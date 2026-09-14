// Package autodl implements the batch download mode of tdl.
//
// It is a native port of the python/run_unified.py helper script, so it reads
// the very same config.json. Instead of shelling out to one tdl process per
// batch it drives tdl's own downloader, which means one connection pool, one
// Telegram client and batched message resolution for the whole run.
package autodl

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
	"gopkg.in/yaml.v3"
)

// Defaults mirror the constants of the python script.
const (
	// DefaultConfigFile is the config name looked up in the working directory
	// when no explicit config is given.
	DefaultConfigFile = "config.json"
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
)

// Config is the python-compatible config.json.
//
// Only "jobs" is required. Unknown fields are rejected so a typo does not
// silently download nothing.
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
	// Chat optionally overrides the chat used for export in incremental mode.
	Chat string `json:"chat" yaml:"chat"`
	// Subdir is the directory under DownloadBase for this job.
	Subdir string `json:"subdir" yaml:"subdir"`

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
// If the file has no .json suffix, a YAML file is read instead. YAML is a
// superset of JSON, so this only widens what is accepted.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "read config %s", path)
	}

	var c Config
	if err = yaml.Unmarshal(b, &c); err != nil {
		return nil, errors.Wrapf(err, "parse config %s", path)
	}

	if err = c.Normalize(); err != nil {
		return nil, err
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

	for i := range c.Jobs {
		if err := c.Jobs[i].normalize(c.DownloadBase, c.Incremental); err != nil {
			return errors.Wrapf(err, "job %d", i+1)
		}
	}

	return nil
}

func (j *Job) normalize(base string, globalIncremental bool) error {
	if strings.TrimSpace(j.ChatURL) == "" {
		return errors.New("chat_url is required")
	}

	if _, err := ParseLink(j.ChatURL); err != nil {
		return err
	}

	if j.Subdir != "" {
		j.dir = filepath.Join(base, j.Subdir)
	} else {
		j.dir = base
	}

	if j.StartComment == nil {
		if v, ok := IntValue(j.Comment); ok && v > 0 {
			j.StartComment = &v
		}
	}

	if !j.UsesIncremental(globalIncremental) {
		if j.StartComment == nil || j.EndComment == nil {
			// a range job needs both ends; incremental jobs get ids from the
			// export instead
			return errors.New("start_comment and end_comment are required unless incremental mode is enabled")
		}

		if *j.EndComment <= *j.StartComment {
			return errors.Errorf("end_comment(%d) must be greater than start_comment(%d)", *j.EndComment, *j.StartComment)
		}
	}

	return nil
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
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
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
	MessageID int
	// Comment is the comment id when the link points into the discussion
	// group, zero otherwise.
	Comment int
}

// CommentMode reports whether the link points at a comment.
func (l Link) CommentMode() bool { return l.Comment > 0 }

// ParseLink parses the telegram links the python script accepts:
//
//	https://t.me/channel/123
//	https://t.me/c/123456789/123
//	https://t.me/channel/123?comment=456
//	t.me/channel/123?thread=99
func ParseLink(raw string) (Link, error) {
	var l Link

	s := strings.TrimSpace(raw)
	if s == "" {
		return l, errors.New("empty telegram link")
	}

	if !strings.Contains(s, "://") {
		s = "https://" + strings.TrimPrefix(s, "//")
	}

	u, err := url.Parse(s)
	if err != nil {
		return l, errors.Wrapf(err, "parse link %q", raw)
	}

	if host := strings.ToLower(u.Hostname()); host != "t.me" && host != "telegram.me" && host != "telegram.dog" {
		return l, errors.Errorf("unsupported link host %q (expected t.me)", u.Hostname())
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return l, errors.Errorf("invalid telegram link %q", raw)
	}

	if strings.EqualFold(parts[0], "c") { // private channel: /c/<id>/<msg>
		if len(parts) < 3 {
			return l, errors.Errorf("invalid private channel link %q", raw)
		}
		l.Chat = parts[1]
		if l.MessageID, err = strconv.Atoi(parts[2]); err != nil {
			return l, errors.Wrapf(err, "invalid message id in %q", raw)
		}
	} else {
		l.Chat = parts[0]
		if len(parts) >= 2 {
			if l.MessageID, err = strconv.Atoi(parts[1]); err != nil {
				return l, errors.Wrapf(err, "invalid message id in %q", raw)
			}
		}
	}

	if c := u.Query().Get("comment"); c != "" {
		if l.Comment, err = strconv.Atoi(c); err != nil {
			return l, errors.Wrapf(err, "invalid comment id in %q", raw)
		}
	}

	return l, nil
}

// FindConfig returns the first existing config candidate in the working
// directory.
func FindConfig() (string, bool) {
	candidates := []string{DefaultConfigFile, "config.yaml", "config.yml"}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, true
		}
	}

	return "", false
}
