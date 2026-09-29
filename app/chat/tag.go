package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/app/dl"
	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
)

// TagOptions selects media posts by the hashtag in their Telegram caption.
type TagOptions struct {
	Chat        string
	Tag         string
	Tags        []string
	TagMatch    string // any (default) or all
	Dir         string
	CheckOnly   bool
	Takeout     bool
	Threads     int
	Limit       int
	PoolSize    int
	PoolSizeSet bool
	Pool        dcpool.Pool
	MaxPosts    int // zero scans the complete chat history
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
	chat, err := tagChatName(opts.Chat)
	if err != nil {
		return err
	}
	if opts.MaxPosts < 0 {
		return fmt.Errorf("max posts must not be negative")
	}
	tags, err := normalizeTags(opts.Tag, opts.Tags)
	if err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(opts.TagMatch))
	if mode == "" {
		mode = "any"
	}
	if mode != "any" && mode != "all" {
		return fmt.Errorf("tag match mode must be any or all")
	}
	manager := peers.Options{Storage: storage.NewPeers(kvd)}.Build(c.API())
	peer, err := tutil.GetInputPeer(ctx, manager, chat)
	if err != nil {
		return fmt.Errorf("resolve chat %q: %w", chat, err)
	}

	it := messages.NewIterator(query.NewQuery(c.API()).Messages().GetHistory(peer.InputPeer()), 100)
	var pending []tagMedia
	var posts []tagPost
	scanned := 0
	flush := func() {
		if len(pending) == 0 {
			return
		}
		if post, ok := matchAlbum(pending, tags, mode, peer.ID(), chat); ok {
			posts = append(posts, post)
		}
		pending = nil
	}
	for it.Next(ctx) {
		if opts.MaxPosts > 0 && len(posts) >= opts.MaxPosts {
			break
		}
		m, ok := it.Value().Msg.(*tg.Message)
		if !ok {
			flush()
			continue
		}
		scanned++
		media, ok := photoOrVideo(m)
		if !ok {
			flush()
			continue
		}
		groupID, _ := m.GetGroupedID()
		if len(pending) > 0 && (groupID == 0 || pending[0].GroupedID != groupID) {
			flush()
			if opts.MaxPosts > 0 && len(posts) >= opts.MaxPosts {
				break
			}
		}
		pending = append(pending, tagMedia{
			ID: m.ID, Type: "message", File: media.Name, Size: media.Size, Date: m.Date,
			Text: m.Message, GroupedID: groupID,
		})
		if groupID == 0 {
			flush()
		}
	}
	if err := it.Err(); err != nil {
		return fmt.Errorf("scan chat history: %w", err)
	}
	flush()
	fmt.Printf("Scanned %d messages; found %d posts matching %s (%s).\n", scanned, len(posts), strings.Join(tags, ", "), mode)
	if opts.CheckOnly {
		for _, post := range posts {
			fmt.Printf("  %d: %d photo/video file(s), %s\n", post.MessageID, len(post.Messages), post.Directory)
		}
		return nil
	}
	if len(posts) == 0 {
		return nil
	}

	root := filepath.Join(opts.Dir, strconv.FormatInt(peer.ID(), 10))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	groupDirByMessage := make(map[int]string)
	manifest := struct {
		ID       int64     `json:"id"`
		Messages []Message `json:"messages"`
	}{ID: peer.ID()}
	for _, post := range posts {
		dir := filepath.Join(root, post.Directory)
		if err := migrateTagDirectory(root, dir, post.MessageID); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "message.txt"), []byte(post.Text), 0o644); err != nil {
			return err
		}
		if err := writeTagJSON(filepath.Join(dir, "message.json"), post); err != nil {
			return err
		}
		for _, m := range post.Messages {
			groupDirByMessage[m.ID] = post.Directory
			manifest.Messages = append(manifest.Messages, Message{
				ID: m.ID, Type: "message", File: m.File, Date: m.Date, Text: m.Text,
			})
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(tags, "|") + ":" + mode))
	manifestPath := filepath.Join(root, fmt.Sprintf("index_%x.json", sum[:6]))
	if err := writeTagJSON(manifestPath, manifest); err != nil {
		return err
	}
	fmt.Printf("Archive: %s\n", root)
	return dl.Run(ctx, c, kvd, dl.Options{
		Dir: root, Files: []string{manifestPath}, Continue: true,
		SkipSame: true, Takeout: opts.Takeout,
		Threads: opts.Threads, Limit: opts.Limit,
		PoolSize: opts.PoolSize, PoolSizeSet: opts.PoolSizeSet,
		Pool:              opts.Pool,
		Template:          `{{.GroupDir}}/{{.MessageID}}_{{filenamify .FileName}}`,
		GroupDirByMessage: groupDirByMessage,
	})
}

func tagChatName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("chat is required")
	}
	if !strings.Contains(raw, "/") && !strings.Contains(raw, ".") {
		return strings.TrimPrefix(raw, "@"), nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if host := strings.ToLower(u.Hostname()); host != "t.me" && host != "telegram.me" && host != "telegram.dog" {
		return "", fmt.Errorf("unsupported chat link host %q", host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 1 || parts[0] == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("--chat must identify a chat, not a message")
	}
	return parts[0], nil
}

func normalizeTag(raw string) (string, error) {
	tag := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if tag == "" {
		return "", fmt.Errorf("tag is required")
	}
	for _, r := range tag {
		if !tagChar(r) {
			return "", fmt.Errorf("invalid hashtag %q", raw)
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
		return nil, fmt.Errorf("at least one tag is required")
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
	if len(matched) == 0 || (mode == "all" && len(matched) != len(tags)) {
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
func migrateTagDirectory(root, target string, id int) error {
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var source string
	for _, entry := range entries {
		if !entry.IsDir() || !(entry.Name() == fmt.Sprintf("message_%d", id) || strings.HasSuffix(entry.Name(), fmt.Sprintf(" [%d]", id))) {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		b, err := os.ReadFile(filepath.Join(candidate, "message.json"))
		if err != nil {
			continue
		}
		var meta struct {
			MessageID int `json:"message_id"`
		}
		if json.Unmarshal(b, &meta) != nil || meta.MessageID != id {
			continue
		}
		if source != "" {
			return fmt.Errorf("multiple existing archives for message %d", id)
		}
		source = candidate
	}
	if source != "" {
		if err := os.Rename(source, target); err != nil {
			return fmt.Errorf("migrate archive %s: %w", source, err)
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

func writeTagJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
