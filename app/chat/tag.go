package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/spf13/viper"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/internal/transfer"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/messages"
)

// TagOptions selects media posts by the hashtag in their Telegram caption.
type TagOptions struct {
	Chat            string
	TopicID         int // zero scans the complete chat
	Tag             string
	Tags            []string
	TagMatch        string // any (default) or all
	Dir             string
	CheckOnly       bool
	Takeout         bool
	Threads         int
	Limit           int
	PoolSize        int
	PoolSizeSet     bool
	Pool            dcpool.Pool
	Manager         *peers.Manager
	Delay           time.Duration
	Reservations    *transfer.Reservations
	Account         string // stable account scope supplied by batch; empty preserves standalone compatibility
	AccountVerified bool   // Account already includes an authenticated Telegram user ID
	MaxPosts        int    // zero scans the complete chat history
	Window          ArchiveWindow

	WriteMetadata *bool // nil enables meta.json output
	OnResolved    func(string)
	OnResult      func(transfer.Counts)
	counts        *transfer.Counts
}

type tagMedia struct {
	ID        int    `json:"id"`
	Type      string `json:"type"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
	Date      int    `json:"date"`
	Text      string `json:"text"`
	GroupedID int64  `json:"grouped_id,omitempty"`
}

type tagPost struct {
	ChatID      int64      `json:"chat_id"`
	Source      string     `json:"source,omitempty"`
	Account     string     `json:"account,omitempty"`
	MessageID   int        `json:"message_id"`
	GroupedID   int64      `json:"grouped_id,omitempty"`
	Tags        []string   `json:"tags"`
	MatchedTags []string   `json:"matched_tags"`
	SourceURL   string     `json:"source_url,omitempty"`
	Text        string     `json:"text"`
	Directory   string     `json:"directory"`
	Messages    []tagMedia `json:"messages"`
}

// DownloadTag walks the chat history so album members are included even when
// Telegram only places a caption on one photo or video in the album.
func DownloadTag(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts TagOptions) error {
	account, err := archiveAccount(ctx, c, opts.Account, opts.AccountVerified)
	if err != nil {
		return err
	}
	opts.Account = account
	return downloadTag(ctx, c.API(), c, kvd, opts)
}

func downloadTag(ctx context.Context, api *tg.Client, c *telegram.Client, kvd storage.Storage, opts TagOptions) (rerr error) {
	opts.counts = &transfer.Counts{}
	defer func() {
		if opts.OnResult != nil {
			opts.OnResult(*opts.counts)
		}
	}()
	target, err := ParseTagTarget(opts.Chat, opts.TopicID)
	if err != nil {
		return err
	}
	if opts.MaxPosts < 0 {
		return diagnostic.Describe(fmt.Errorf("max posts must not be negative"), corei18n.Message{ID: "errors.message.max_posts_must_not_be_negative"})
	}
	tags, err := normalizeTags(opts.Tag, opts.Tags)
	if err != nil {
		return err
	}
	mode, err := ParseTagMatch(opts.TagMatch)
	if err != nil {
		return err
	}
	manager := opts.Manager
	if manager == nil {
		manager = peers.Options{Storage: storage.NewPeers(kvd)}.Build(api)
	}
	chat := target.Chat
	peer, err := tutil.GetInputPeer(ctx, manager, chat)
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("resolve chat %q: %w", chat, err), corei18n.Message{ID: "errors.message.resolve_chat_value_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", chat), "Arg2": err}})
	}
	history, topic, err := tagHistory(ctx, api, archiveInputPeer(peer), target.TopicID)
	if err != nil {
		return err
	}
	topicTitle := ""
	if topic != nil {
		topicTitle = topic.Title
	}
	announceTarget(ctx, TargetNameContext(ctx, peer, target.TopicID, topicTitle, target.TopicID == 0), opts.OnResolved)
	if !opts.CheckOnly && opts.Pool == nil {
		if c == nil {
			return diagnostic.Describe(fmt.Errorf("tag archive requires a download pool"), corei18n.Message{ID: "errors.message.tag_archive_requires_a_download_pool"})
		}
		poolSize := opts.PoolSize
		if !opts.PoolSizeSet {
			poolSize = viper.GetInt(consts.FlagPoolSize)
		}
		opts.Pool = dcpool.NewPool(c, int64(poolSize), tclient.NewDefaultMiddlewares(ctx, viper.GetDuration(consts.FlagReconnectTimeout))...)
		defer multierr.AppendInvoke(&rerr, multierr.Close(opts.Pool))
	}
	if opts.Reservations == nil {
		opts.Reservations = &transfer.Reservations{}
	}
	root := filepath.Join(opts.Dir, strconv.FormatInt(peer.ID(), 10))
	scope := strings.Join(tags, "|") + ":" + mode
	if target.TopicID > 0 {
		scope += ":topic:" + strconv.Itoa(target.TopicID)
	}
	sum := sha256.Sum256([]byte(scope))
	manifestPath := filepath.Join(root, fmt.Sprintf("index_%x.json", sum[:6]))
	var index *tagIndex
	defer func() {
		if index != nil {
			rerr = multierr.Append(rerr, index.abort())
		}
	}()
	delay := &transfer.Delay{Duration: opts.Delay}
	selected, scanned := 0, 0
	complete, err := scanArchiveHistory(ctx, history, opts.Window, func(album []*tg.Message) (bool, error) {
		var pending []tagMedia
		for _, m := range album {
			scanned++
			opts.counts.Messages++
			if media, ok := photoOrVideo(m); ok {
				pending = append(pending, tagMedia{ID: m.ID, Type: "message", File: media.Name, Size: media.Size, Date: m.Date, Text: m.Message, GroupedID: m.GroupedID})
			}
		}
		if post, ok := matchAlbumIfMedia(pending, tags, mode, peer.ID(), chat); ok {
			post.Source, post.Account = archiveSource(archiveInputPeer(peer)), opts.Account
			selected++
			opts.counts.Posts++
			if opts.CheckOnly {
				opts.counts.Files += int64(len(post.Messages))
				fmt.Println(console.Translate(ctx, messages.TagArchiveFile(post.MessageID, len(post.Messages), post.Directory)))
			} else {
				if index == nil {
					if err := os.MkdirAll(root, 0o755); err != nil {
						return false, err
					}
					index, err = newTagIndex(manifestPath, peer.ID(), opts.Reservations)
					if err != nil {
						return false, err
					}
					fmt.Println(console.Translate(ctx, messages.TagArchiveRoot(root)))
				}
				if err := archiveTagPost(ctx, root, post, album, archiveInputPeer(peer), opts, delay); err != nil {
					return false, err
				}
				for _, m := range post.Messages {
					if err := index.append(m); err != nil {
						return false, err
					}
				}
			}
			if opts.MaxPosts > 0 && selected >= opts.MaxPosts {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	fmt.Println(console.Translate(ctx, messages.TagScanSummary(scanned, selected, strings.Join(tags, ", "), mode)))
	if opts.CheckOnly {
		return nil
	}
	if index != nil {
		if err := index.commit(); err != nil {
			return err
		}
	}
	if !complete && opts.Window.Until > 0 {
		return diagnostic.Describe(fmt.Errorf("max_posts truncated the incremental archive window; keeping last_ts"), corei18n.Message{ID: "errors.message.max_key_posts_truncated_the_incremental_archive_window_keeping_last_key_ts"})
	}
	return nil
}

// Tag match modes for posts selected by multiple tags.
const (
	TagMatchAny = "any"
	TagMatchAll = "all"
)

// ParseTagMatch normalizes a tag match mode; empty selects TagMatchAny.
func ParseTagMatch(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "":
		return TagMatchAny, nil
	case TagMatchAny, TagMatchAll:
		return mode, nil
	default:
		return "", diagnostic.Describe(fmt.Errorf("tag_match must be any or all"), corei18n.Message{ID: "errors.message.tag_key_match_must_be_any_or_all"})
	}
}

func matchAlbumIfMedia(pending []tagMedia, tags []string, mode string, id int64, chat string) (tagPost, bool) {
	if len(pending) == 0 {
		return tagPost{}, false
	}
	return matchAlbum(pending, tags, mode, id, chat)
}

func normalizeTag(raw string) (string, error) {
	tag := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if tag == "" {
		return "", diagnostic.Describe(fmt.Errorf("tag is required"), corei18n.Message{ID: "errors.message.tag_is_required"})
	}
	for _, r := range tag {
		if !tagChar(r) {
			return "", diagnostic.Describe(fmt.Errorf("invalid hashtag %q", raw), corei18n.Message{ID: "errors.message.invalid_hashtag_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", raw)}})
		}
	}
	return "#" + tag, nil
}

func normalizeTags(single string, multiple []string) ([]string, error) {
	inputs := append([]string(nil), multiple...)
	if strings.TrimSpace(single) != "" {
		inputs = append([]string{single}, inputs...)
	}
	if len(inputs) == 0 {
		return nil, diagnostic.Describe(fmt.Errorf("at least one tag is required"), corei18n.Message{ID: "errors.message.at_least_one_tag_is_required"})
	}
	tags := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		tag, err := normalizeTag(input)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(tag)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		tags = append(tags, tag)
	}
	return tags, nil
}

func tagChar(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func hasTag(content, tag string) bool {
	for i, r := range content {
		if r != '#' {
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(content[:i])
			if tagChar(prev) {
				continue
			}
		}
		end := i + 1
		for end < len(content) {
			next, size := utf8.DecodeRuneInString(content[end:])
			if !tagChar(next) {
				break
			}
			end += size
		}
		if strings.EqualFold(content[i:end], tag) {
			return true
		}
	}
	return false
}

// pending arrives newest first; reverse it to preserve the original album order.
func matchAlbum(pending []tagMedia, tags []string, mode string, chatID int64, chat string) (tagPost, bool) {
	var caption string
	matched := make([]string, 0, len(tags))
	for _, tag := range tags {
		found := false
		for i := len(pending) - 1; i >= 0; i-- {
			if hasTag(pending[i].Text, tag) {
				found = true
				if caption == "" {
					caption = pending[i].Text
				}
			}
		}
		if found {
			matched = append(matched, tag)
		}
	}
	if len(matched) == 0 || (mode == TagMatchAll && len(matched) != len(tags)) {
		return tagPost{}, false
	}
	media := make([]tagMedia, len(pending))
	for i, m := range pending {
		media[len(pending)-1-i] = m
	}
	post := tagPost{
		ChatID: chatID, MessageID: media[0].ID,
		GroupedID: media[0].GroupedID, Tags: tags, MatchedTags: matched,
		Text: caption, Messages: media,
	}
	post.Directory = postDirectory(caption, post.MessageID, matched[0])
	if _, err := strconv.ParseInt(chat, 10, 64); err != nil {
		post.SourceURL = fmt.Sprintf("https://t.me/%s/%d", chat, post.MessageID)
	} else {
		post.SourceURL = fmt.Sprintf("https://t.me/c/%s/%d", chat, post.MessageID)
	}
	return post, true
}

// postDirectory uses the caption as the visible folder name. A caption made
// only of hashtags uses those hashtags in their original order, without #.
// The first message ID remains a unique suffix for repeated captions.
func postDirectory(caption string, id int, matchedTag string) string {
	var out strings.Builder
	var captionTags []string
	for i := 0; i < len(caption); {
		r, size := utf8.DecodeRuneInString(caption[i:])
		if r == '#' {
			end := i + size
			for end < len(caption) {
				next, n := utf8.DecodeRuneInString(caption[end:])
				if !tagChar(next) {
					break
				}
				end += n
			}
			if end > i+size {
				captionTags = append(captionTags, caption[i+size:end])
				out.WriteRune(' ')
				i = end
				continue
			}
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			out.WriteRune(' ')
		} else {
			out.WriteRune(r)
		}
		i += size
	}
	title := strings.Join(strings.Fields(out.String()), " ")
	title = strings.Trim(title, " ._-")
	tagOnly := len(captionTags) > 0 && strings.TrimFunc(title, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	}) == ""
	if tagOnly {
		title = strings.Join(captionTags, " ")
	}
	runes := []rune(title)
	if len(runes) > 64 {
		title = strings.Trim(string(runes[:64]), " ._-")
	}
	if title == "" {
		title = "post"
	}
	if matchedTag != "" && !tagOnly {
		title = strings.TrimPrefix(matchedTag, "#") + " " + title
	}
	runes = []rune(title)
	if len(runes) > 64 {
		title = strings.Trim(string(runes[:64]), " ._-")
	}
	return fmt.Sprintf("%s [%d]", title, id)
}

// migrateTagDirectory moves an archive made with an older folder naming rule
// only when its metadata confirms the same Telegram message ID.
func migrateTagDirectory(root, target string, post tagPost) error {
	return migrateTagDirectoryContext(context.Background(), root, target, post)
}

func migrateTagDirectoryContext(ctx context.Context, root, target string, post tagPost) error {
	id := post.MessageID
	if _, err := os.Stat(target); err == nil {
		return checkArchiveOwner(target, post)
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var source string
	legacyName, suffix := fmt.Sprintf("message_%d", id), fmt.Sprintf(" [%d]", id)
	for _, entry := range entries {
		if !entry.IsDir() || (entry.Name() != legacyName && !strings.HasSuffix(entry.Name(), suffix)) {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		b, err := readArchiveMetadata(candidate)
		if err != nil {
			continue
		}
		var meta tagPost
		if json.Unmarshal(b, &meta) != nil || !sameArchiveOwner(meta, post) {
			fmt.Println(console.Translate(ctx, messages.TagArchiveUnverified(candidate)))
			continue
		}
		if source != "" {
			return diagnostic.Describe(fmt.Errorf("multiple existing archives for message %d", id), corei18n.Message{ID: "errors.message.multiple_existing_archives_for_message_value", Args: map[string]any{"Arg1": id}})
		}
		source = candidate
	}
	if source != "" {
		if err := os.Rename(source, target); err != nil {
			return diagnostic.Describe(fmt.Errorf("migrate archive %s: %w", source, err), corei18n.Message{ID: "errors.message.migrate_archive_value_value", Args: map[string]any{"Arg1": source, "Arg2": err}})
		}
	}
	return nil
}

func photoOrVideo(m *tg.Message) (*tmedia.Media, bool) {
	md, ok := m.GetMedia()
	if !ok {
		return nil, false
	}
	switch v := md.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := v.Photo.(*tg.Photo)
		if !ok || len(p.Sizes) == 0 {
			return nil, false
		}
	case *tg.MessageMediaDocument:
		d, ok := v.Document.(*tg.Document)
		if !ok {
			return nil, false
		}
		imageOrVideo := strings.HasPrefix(d.MimeType, "image/") || strings.HasPrefix(d.MimeType, "video/")
		for _, attr := range d.Attributes {
			if _, ok := attr.(*tg.DocumentAttributeVideo); ok {
				imageOrVideo = true
			}
		}
		if !imageOrVideo {
			return nil, false
		}
	default:
		return nil, false
	}
	return tmedia.GetMedia(m)
}
