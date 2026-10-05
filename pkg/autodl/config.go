// Package autodl implements the batch download mode of tdl.
//
// It is a native port of the python/run_unified.py helper script, so it reads
// the same configuration fields as the Python JSON config. Instead of shelling
// out to one tdl process per batch it drives tdl's own downloader, which means
// one connection pool, one Telegram client and batched message resolution.
package autodl

import (
	"bytes"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
	"gopkg.in/yaml.v3"

	"github.com/iyear/tdl/app/chat"
	"github.com/iyear/tdl/core/util/tgref"
)

// Defaults mirror the constants of the python script.
const (
	// DefaultConfigFile is the config name looked up in the working directory
	// when no explicit config is given.
	DefaultConfigFile = "config.yaml"
	// DefaultDownloadBase is the download root used when the config does not
	// set one.
	DefaultDownloadBase = "downloads"
	// DefaultStateFile keeps the incremental timestamps on disk, like the
	// python script does.
	DefaultStateFile = "tdl_state.json"
	// DefaultOverlapSeconds is the incremental lookback window. It protects
	// against delayed and re-sent messages.
	DefaultOverlapSeconds = 3600

	// DefaultPoolSize, DefaultThreads and DefaultLimit are the connection pool,
	// per file thread count and concurrent task count used when the config
	// does not specify them.
	DefaultPoolSize = 16
	DefaultThreads  = 8
	DefaultLimit    = 4

	// DefaultBatchSize is how many message ids are fetched (and resolved) per
	// Telegram request.
	DefaultBatchSize = 100
	// MaxRangeMessages prevents a malformed range from allocating enough memory
	// to terminate the process before any Telegram request is made.
	MaxRangeMessages = 1_000_000
)

// Config is compatible with the Python batch configuration fields.
//
// Only "jobs" is required. Unknown fields are ignored for compatibility with
// config generators that also store settings used by other tools.
type Config struct {
	// Namespace is the tdl namespace (account) to use. It maps to -n/--ns and
	// defaults to "default" when empty.
	Namespace string `json:"namespace" yaml:"namespace"`
	// DownloadBase is the download root. "download_dir" is accepted as an
	// alias to stay compatible with older configs.
	DownloadBase string `json:"download_base" yaml:"download_base"`
	DownloadDir  string `json:"download_dir" yaml:"download_dir"`
	// Incremental enables the timestamp based incremental mode for every job
	// that does not override it.
	Incremental bool `json:"incremental" yaml:"incremental"`
	// WriteMetadata controls per-post meta.json output in archive jobs.
	// Omission enables metadata; a job level setting takes precedence.
	WriteMetadata *bool `json:"write_metadata,omitempty" yaml:"write_metadata,omitempty"`
	// StateFile overrides where the incremental timestamps are stored.
	StateFile string `json:"state_file" yaml:"state_file"`
	// OverlapSeconds overrides the incremental lookback window.
	OverlapSeconds *int `json:"overlap_seconds" yaml:"overlap_seconds"`
	// Jobs are the download tasks, processed in order.
	Jobs []Job `json:"jobs" yaml:"jobs"`

	// Pool, Threads and Limit are optional tdl performance overrides. They
	// mirror --pool, -t/--threads and -l/--limit.
	Pool    *int `json:"pool,omitempty" yaml:"pool,omitempty"`
	Threads *int `json:"threads,omitempty" yaml:"threads,omitempty"`
	Limit   *int `json:"limit,omitempty" yaml:"limit,omitempty"`
}

// Job is a single download task.
type Job struct {
	// ChatURL is the telegram message link, e.g.
	// https://t.me/channel/123 or https://t.me/channel/1?comment=456.
	ChatURL string `json:"chat_url" yaml:"chat_url"`
	// FollowLinks archives resources reached through main-post or comment links.
	FollowLinks bool             `json:"follow_links" yaml:"follow_links"`
	LinkOptions chat.LinkOptions `json:"link_options" yaml:"link_options"`
	// Tag selects captioned photo/video posts inside the job's scan window.
	// Without a range or incremental mode, archive jobs scan all history.
	Tag      string   `json:"tag" yaml:"tag"`
	Tags     []string `json:"tags" yaml:"tags"`
	TagMatch string   `json:"tag_match" yaml:"tag_match"`
	// MaxPosts limits tag jobs for a bounded preview; zero scans all history.
	MaxPosts int `json:"max_posts" yaml:"max_posts"`
	// Chat optionally overrides the chat used for export in incremental mode.
	Chat string `json:"chat" yaml:"chat"`
	// Subdir is the directory under DownloadBase for this job.
	Subdir string `json:"subdir" yaml:"subdir"`
	// WriteMetadata overrides the global archive metadata switch.
	WriteMetadata *bool `json:"write_metadata,omitempty" yaml:"write_metadata,omitempty"`

	// Comment selects the comment mode (?comment=N). It follows the python
	// semantics: a bool, or an int used as the starting comment id when
	// start_comment is absent.
	Comment any `json:"comment" yaml:"comment"`
	// StartComment is the inclusive first message/comment id of the range.
	StartComment *int `json:"start_comment" yaml:"start_comment"`
	// EndComment is the exclusive last message/comment id of the range.
	EndComment *int `json:"end_comment" yaml:"end_comment"`

	// Incremental overrides the global incremental switch for this job.
	Incremental *bool `json:"incremental" yaml:"incremental"`

	// Export options used by incremental mode.
	ExportFilter string `json:"export_filter" yaml:"export_filter"`
	WithContent  bool   `json:"with_content" yaml:"with_content"`
	ExportAll    bool   `json:"export_all" yaml:"export_all"`
	TopicID      *int   `json:"topic_id" yaml:"topic_id"`
	ReplyPostID  *int   `json:"reply_post_id" yaml:"reply_post_id"`
	Overlap      *int   `json:"overlap_seconds" yaml:"overlap_seconds"`

	// dir is the resolved download directory, set by Normalize.
	dir string
	// mode is the resolved download mode, set by the runner from --mode and
	// the config. Empty until then. See ResolveMode.
	mode string
}

// Dir returns the resolved download directory of the job.
func (j *Job) Dir() string { return j.dir }

// LoadConfig reads and normalizes a config file.
//
// YAML is a superset of JSON, so both YAML and legacy JSON config files are
// accepted regardless of their filename extension.
func LoadConfig(path string) (*Config, error) {
	return loadConfig(path, false)
}

// LoadConfigForRun loads a config while applying command-line modes that
// affect validation. In particular, --incremental makes ranges optional.
func LoadConfigForRun(path string, forceIncremental bool) (*Config, error) {
	return loadConfig(path, forceIncremental)
}

func loadConfig(path string, forceIncremental bool) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, &ConfigError{Path: path, Err: errors.Wrap(err, "read config")}
	}

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err = dec.Decode(&c); err != nil {
		return nil, &ConfigError{Path: path, Err: errors.Wrap(err, "parse config")}
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple YAML documents are not supported")
		}
		return nil, &ConfigError{Path: path, Err: errors.Wrap(err, "parse config")}
	}
	if forceIncremental {
		for i := range c.Jobs {
			on := true
			c.Jobs[i].Incremental = &on
		}
	}

	if err = c.Normalize(); err != nil {
		var configErr *ConfigError
		if errors.As(err, &configErr) {
			configErr.Path = path
			return nil, configErr
		}
		return nil, &ConfigError{Path: path, Err: err}
	}

	return &c, nil
}

// Normalize validates the config and resolves the derived fields.
func (c *Config) Normalize() error {
	if len(c.Jobs) == 0 {
		return errors.New("config has no jobs")
	}

	if c.DownloadBase == "" {
		c.DownloadBase = c.DownloadDir
	}
	if c.DownloadBase == "" {
		c.DownloadBase = DefaultDownloadBase
	}
	for name, value := range map[string]*int{
		"threads": c.Threads, "limit": c.Limit,
	} {
		if value != nil && *value <= 0 {
			return errors.Errorf("%s must be positive", name)
		}
	}
	if c.Pool != nil && *c.Pool < 0 {
		return errors.New("pool must not be negative")
	}
	if c.OverlapSeconds != nil && *c.OverlapSeconds < 0 {
		return errors.New("overlap_seconds must not be negative")
	}

	for i := range c.Jobs {
		if err := c.Jobs[i].normalize(c.DownloadBase, c.Incremental); err != nil {
			return &ConfigError{Job: i + 1, ChatURL: c.Jobs[i].ChatURL, Err: err}
		}
	}

	return nil
}

func (j *Job) normalize(base string, globalIncremental bool) error {
	if strings.TrimSpace(j.ChatURL) == "" {
		return errors.New("chat_url is required")
	}
	if j.Comment != nil {
		if _, ok := j.Comment.(bool); !ok {
			if _, text := j.Comment.(string); text {
				return errors.New("comment must be a boolean or integer")
			}
			if value, ok := IntValue(j.Comment); !ok || value <= 0 {
				return errors.New("comment must be a boolean or positive integer")
			}
		}
	}
	for field, value := range map[string]*int{"topic_id": j.TopicID, "reply_post_id": j.ReplyPostID} {
		if value != nil && *value <= 0 {
			return errors.Errorf("%s must be positive", field)
		}
	}
	if j.TopicID != nil && j.ReplyPostID != nil {
		return errors.New("topic_id and reply_post_id cannot be combined")
	}

	if !j.IsTagJob() || j.FollowLinks {
		if _, err := ParseLink(j.ChatURL); err != nil {
			return err
		}
	}

	if j.Subdir != "" {
		clean := filepath.Clean(j.Subdir)
		if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return errors.Errorf("subdir %q must stay inside download_base", j.Subdir)
		}
		j.dir = filepath.Join(base, j.Subdir)
	} else {
		j.dir = base
	}

	if j.StartComment == nil {
		if v, ok := IntValue(j.Comment); ok && v > 0 {
			j.StartComment = &v
		}
	}
	if j.StartComment != nil && *j.StartComment <= 0 {
		return errors.New("start_comment must be positive")
	}
	if j.EndComment != nil && *j.EndComment <= 0 {
		return errors.New("end_comment must be positive")
	}
	if j.Overlap != nil && *j.Overlap < 0 {
		return errors.New("overlap_seconds must not be negative")
	}

	if j.FollowLinks {
		link, _ := ParseLink(j.ChatURL)
		if link.Comment != 0 || j.CommentMode() || j.TopicID != nil || j.ReplyPostID != nil {
			return errors.New("follow_links source must be a main chat/post; comment/topic selectors are not supported")
		}
		if err := j.validateArchiveRange(globalIncremental); err != nil {
			return err
		}
		if !j.UsesIncremental(globalIncremental) && link.MessageID > 0 && j.StartComment != nil {
			return errors.New("follow_links cannot combine a post URL and a range")
		}
		if j.MaxPosts < 0 {
			return errors.New("max_posts must not be negative")
		}
		if j.TagMatch != "" && j.TagMatch != "any" && j.TagMatch != "all" {
			return errors.New("tag_match must be any or all")
		}
		if j.Tag != "" && strings.TrimSpace(j.Tag) == "" {
			return errors.New("tag must not be blank")
		}
		for _, tag := range j.Tags {
			if strings.TrimSpace(tag) == "" {
				return errors.New("tags must not contain blanks")
			}
		}
		return j.LinkOptions.Normalize()
	}

	if j.IsTagJob() {
		if j.Tag != "" && strings.TrimSpace(j.Tag) == "" {
			return errors.New("tag must not be blank")
		}
		if j.TopicID != nil && *j.TopicID <= 0 {
			return errors.New("topic_id must be positive")
		}
		if _, err := chat.ParseTagTarget(j.ChatURL, num(j.TopicID)); err != nil {
			return err
		}
		if j.CommentMode() || j.ReplyPostID != nil {
			return errors.New("tag job cannot select comment mode or reply_post_id")
		}
		if err := j.validateArchiveRange(globalIncremental); err != nil {
			return err
		}
		if j.MaxPosts < 0 {
			return errors.New("max_posts must not be negative")
		}
		if j.TagMatch != "" && j.TagMatch != "any" && j.TagMatch != "all" {
			return errors.New("tag_match must be any or all")
		}
		if j.Tag == "" && len(j.Tags) == 0 {
			return errors.New("tag or tags is required")
		}
		for _, tag := range j.Tags {
			if strings.TrimSpace(tag) == "" {
				return errors.New("tags must not contain blanks")
			}
		}
		return nil
	}

	if !j.UsesIncremental(globalIncremental) {
		if j.TopicID != nil || j.ReplyPostID != nil {
			return errors.New("topic_id and reply_post_id require incremental mode for message jobs")
		}
		if j.StartComment == nil || j.EndComment == nil {
			// a range job needs both ends; incremental jobs get ids from the
			// export instead
			return errors.New("start_comment and end_comment are required unless incremental mode is enabled")
		}

		if *j.EndComment <= *j.StartComment {
			return errors.Errorf("end_comment(%d) must be greater than start_comment(%d)", *j.EndComment, *j.StartComment)
		}
		if int64(*j.EndComment)-int64(*j.StartComment) > MaxRangeMessages {
			return errors.Errorf("message range exceeds the safety limit of %d", MaxRangeMessages)
		}
	}

	return nil
}

func (j *Job) validateArchiveRange(globalIncremental bool) error {
	if j.UsesIncremental(globalIncremental) {
		return nil // incremental mode takes precedence over ID ranges
	}
	if (j.StartComment == nil) != (j.EndComment == nil) {
		return errors.New("archive ranges require both start_comment and end_comment")
	}
	if j.StartComment != nil && (*j.EndComment <= *j.StartComment || int64(*j.EndComment)-int64(*j.StartComment) > MaxRangeMessages) {
		return errors.New("invalid archive message range")
	}
	return nil
}

func (j *Job) IsTagJob() bool { return j.Tag != "" || len(j.Tags) > 0 }

// WritesMetadata reports whether archive jobs should output meta.json.
// A job level setting wins; omission at both levels enables output.
func (j *Job) WritesMetadata(global *bool) bool {
	if j.WriteMetadata != nil {
		return *j.WriteMetadata
	}
	return global == nil || *global
}

// UsesIncremental reports whether the job runs in incremental mode with the
// given config wide default. A job level setting always wins.
func (j *Job) UsesIncremental(global bool) bool {
	if j.Incremental != nil {
		return *j.Incremental
	}

	return global
}

// CommentMode reports whether the message ids of the job are comments in the
// linked discussion group rather than messages of the chat itself.
//
// A resolved mode (-mode) wins over the config, so --mode comment/direct can
// override the "comment" key of a job.
func (j *Job) CommentMode() bool {
	switch j.mode {
	case ModeComment:
		return true
	case ModeDirect:
		return false
	}

	if j.Comment == nil {
		return false
	}

	if b, ok := j.Comment.(bool); ok {
		return b
	}

	// an int value is the legacy "start_comment" spelling and always implies
	// comment mode
	_, ok := IntValue(j.Comment)
	return ok
}

// IntValue converts a decoded json scalar into an int.
func IntValue(v any) (int, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case bool:
		return 0, false
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) || math.Trunc(t) != t {
			return 0, false
		}
		i, err := strconv.ParseInt(strconv.FormatFloat(t, 'f', -1, 64), 10, strconv.IntSize)
		return int(i), err == nil
	case int:
		return t, true
	case int64:
		i := int(t)
		return i, int64(i) == t
	case uint64:
		i := int(t)
		return i, i >= 0 && uint64(i) == t
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

// Link is a parsed telegram message link.
type Link struct {
	// Chat is the channel username or the numeric channel id for private
	// links.
	Chat string
	// MessageID is the target post id in Chat.
	// It is zero when the link identifies only a public channel.
	MessageID int
	// Comment is the comment id when the link points into the discussion
	// group, zero otherwise.
	Comment int
}

// CommentMode reports whether the link points at a comment.
func (l Link) CommentMode() bool { return l.Comment > 0 }

// ParseLink parses the telegram links the python script accepts:
//
//	https://t.me/channel
//	https://t.me/channel/123
//	https://t.me/c/123456789/
//	https://t.me/c/123456789/123
//	https://t.me/channel/123?comment=456
//	t.me/channel/123?thread=99
func ParseLink(raw string) (Link, error) {
	ref, err := tgref.Parse(raw, tgref.Options{})
	if err != nil {
		return Link{}, err
	}
	if ref.Bot {
		return Link{}, errors.New("bot start links are not a batch message source")
	}
	return Link{Chat: ref.Chat, MessageID: ref.MessageID, Comment: ref.CommentID}, nil
}

// FindConfig returns the first existing config candidate in the working
// directory.
func FindConfig() (string, bool) {
	candidates := []string{DefaultConfigFile, "config.yml", "config.json"}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, true
		}
	}

	return "", false
}
