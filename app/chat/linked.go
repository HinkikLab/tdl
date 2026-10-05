package chat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tgref"
	"github.com/iyear/tdl/core/util/tutil"
)

// LinkOptions controls bounded resolution of resources linked from a post.
type LinkOptions struct {
	MaxDepth           int   `json:"max_depth" yaml:"max_depth"`
	MaxLinks           int   `json:"max_links" yaml:"max_links"`
	BotTimeout         int   `json:"bot_timeout_seconds" yaml:"bot_timeout_seconds"`
	BotIdle            int   `json:"bot_idle_seconds" yaml:"bot_idle_seconds"`
	PollInterval       int   `json:"poll_interval_ms" yaml:"poll_interval_ms"`
	MaxBotMessages     int   `json:"max_bot_messages" yaml:"max_bot_messages"`
	MaxTopicMessages   int   `json:"max_topic_messages" yaml:"max_topic_messages"`
	ReRequestLimit     *int  `json:"rerequest_limit" yaml:"rerequest_limit"`
	ScanComments       *bool `json:"scan_comments" yaml:"scan_comments"`
	CommentLimit       int   `json:"comment_limit" yaml:"comment_limit"`
	IncludePreviews    *bool `json:"include_previews" yaml:"include_previews"`
	CleanupBotMessages *bool `json:"cleanup_bot_messages" yaml:"cleanup_bot_messages"`
	FloodRetries       *int  `json:"flood_retries" yaml:"flood_retries"`
	FloodWait          int   `json:"flood_wait_seconds" yaml:"flood_wait_seconds"`
	MaxFloodWait       int   `json:"max_flood_wait_seconds" yaml:"max_flood_wait_seconds"`
}

func (o *LinkOptions) Normalize() error {
	for _, field := range []struct {
		name          string
		value         *int
		fallback, max int
	}{
		{"max_depth", &o.MaxDepth, 8, 32},
		{"max_links", &o.MaxLinks, 100, 1000},
		{"bot_timeout_seconds", &o.BotTimeout, 60, 3600},
		{"bot_idle_seconds", &o.BotIdle, 3, 300},
		{"poll_interval_ms", &o.PollInterval, 500, 60000},
		{"max_bot_messages", &o.MaxBotMessages, 500, 10000},
		{"max_topic_messages", &o.MaxTopicMessages, 1000, 100000},
		{"comment_limit", &o.CommentLimit, 100, 10000},
		{"flood_wait_seconds", &o.FloodWait, 30, 3600},
		{"max_flood_wait_seconds", &o.MaxFloodWait, 3600, 86400},
	} {
		if *field.value < 0 || *field.value > field.max {
			return fmt.Errorf("%s must be between 0 and %d", field.name, field.max)
		}
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	if o.BotIdle >= o.BotTimeout {
		return fmt.Errorf("bot_idle_seconds must be less than bot_timeout_seconds")
	}
	if o.PollInterval >= o.BotTimeout*1000 {
		return fmt.Errorf("poll_interval_ms must be less than bot_timeout_seconds")
	}
	if o.ReRequestLimit == nil {
		v := 3
		o.ReRequestLimit = &v
	}
	if *o.ReRequestLimit < 0 || *o.ReRequestLimit > 10 {
		return fmt.Errorf("rerequest_limit must be between 0 and 10")
	}
	if o.FloodRetries == nil {
		v := 5
		o.FloodRetries = &v
	}
	if *o.FloodRetries < 0 || *o.FloodRetries > 20 {
		return fmt.Errorf("flood_retries must be between 0 and 20")
	}
	if o.FloodWait > o.MaxFloodWait {
		return fmt.Errorf("flood_wait_seconds must not exceed max_flood_wait_seconds")
	}
	return nil
}

func defaultOn(v *bool) bool { return v == nil || *v }

type resourceLink struct {
	Kind    string
	Chat    string
	ID      int
	TopicID int
	Start   string
	Comment int
	Single  bool
}

func (l resourceLink) key() string {
	return fmt.Sprintf("%s:%s:%d:%d:%s:%d:%t", l.Kind, strings.ToLower(l.Chat), l.ID, l.TopicID, l.Start, l.Comment, l.Single)
}

func (l resourceLink) URL() string {
	if l.Kind == "bot" {
		return "https://t.me/" + l.Chat + "?start=" + url.QueryEscape(l.Start)
	}
	path := l.Chat
	if _, err := strconv.ParseInt(l.Chat, 10, 64); err == nil {
		path = "c/" + l.Chat
	}
	if l.ID > 0 {
		if l.TopicID > 0 {
			path += "/" + strconv.Itoa(l.TopicID)
		}
		path += "/" + strconv.Itoa(l.ID)
	}
	u := "https://t.me/" + path
	q := url.Values{}
	if l.Comment > 0 {
		q.Set("comment", strconv.Itoa(l.Comment))
	}
	if l.Single {
		q.Set("single", "")
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

var telegramURL = regexp.MustCompile(`(?i)(?:https?://(?:t\.me|telegram\.me|telegram\.dog)/|(?:t\.me|telegram\.me|telegram\.dog)/|tg://)[^\s<>"\x{200b}]+`)
var telegramUsername = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Only message links and bot start links are actionable. Home, invite, payment,
// share and external web links do not identify a resource for this archive.
func parseResourceLink(raw string) (resourceLink, error) {
	ref, err := tgref.Parse(raw, tgref.Options{AllowTG: true})
	if err != nil {
		return resourceLink{}, err
	}
	kind := "chat"
	if ref.Bot {
		kind = "bot"
	} else if ref.MessageID > 0 {
		kind = "message"
	}
	return resourceLink{Kind: kind, Chat: ref.Chat, ID: ref.MessageID, TopicID: ref.TopicID, Start: ref.Start, Comment: ref.CommentID, Single: ref.Single}, nil
}

func utf16Text(s string, offset, length int) string {
	u := utf16.Encode([]rune(s))
	if offset < 0 || length < 0 || offset > len(u) || length > len(u)-offset {
		return ""
	}
	return string(utf16.Decode(u[offset : offset+length]))
}

func messageResourceLinks(m *tg.Message) []resourceLink {
	var raw []string
	for _, e := range m.Entities {
		switch e := e.(type) {
		case *tg.MessageEntityTextURL:
			raw = append(raw, e.URL)
		case *tg.MessageEntityURL:
			raw = append(raw, utf16Text(m.Message, e.Offset, e.Length))
		}
	}
	if keyboard, ok := m.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
		for _, row := range keyboard.Rows {
			for _, b := range row.Buttons {
				if b, ok := b.(*tg.KeyboardButtonURL); ok {
					raw = append(raw, b.URL)
				}
			}
		}
	}
	raw = append(raw, telegramURL.FindAllString(m.Message, -1)...)
	seen := map[string]bool{}
	var links []resourceLink
	for _, s := range raw {
		l, err := parseResourceLink(strings.TrimRight(s, ".,;!，。；！)]）】"))
		if err != nil || l.Kind == "chat" || seen[l.key()] {
			continue
		}
		seen[l.key()] = true
		links = append(links, l)
	}
	return links
}

type resourceMessage struct {
	Peer    tg.InputPeerClass
	Message *tg.Message
}

type linkBackend interface {
	Fetch(context.Context, resourceLink) ([]resourceMessage, error)
	Comments(context.Context, tg.InputPeerClass, *tg.Message, int) ([]resourceMessage, error)
}

type linkedResource struct {
	resourceMessage
	Media *tmedia.Media
}

type linkHop struct {
	URL        string `json:"url"`
	Depth      int    `json:"depth"`
	MessageIDs []int  `json:"message_ids"`
}

type linkResolver struct {
	backend     linkBackend
	opts        LinkOptions
	unavailable *UnavailableLinks
}

func (r *linkResolver) Resolve(ctx context.Context, roots []resourceLink) ([]linkedResource, []linkHop, error) {
	if r.unavailable == nil {
		r.unavailable = &UnavailableLinks{}
	}
	var files []linkedResource
	var hops []linkHop
	done, active, mediaSeen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var walk func(resourceLink, int) error
	walk = func(l resourceLink, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.unavailable.lookup(l); err != nil {
			return err
		}
		if active[l.key()] {
			return fmt.Errorf("resource link cycle at %s", l.URL())
		}
		if done[l.key()] {
			return nil
		}
		if depth > r.opts.MaxDepth {
			return fmt.Errorf("resource link depth exceeds %d", r.opts.MaxDepth)
		}
		if len(done) >= r.opts.MaxLinks {
			return fmt.Errorf("resource link count exceeds %d", r.opts.MaxLinks)
		}
		done[l.key()], active[l.key()] = true, true
		defer delete(active, l.key())
		msgs, err := r.backend.Fetch(ctx, l)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", l.URL(), r.unavailable.remember(l, err))
		}
		hop := linkHop{URL: l.URL(), Depth: depth}
		var next []resourceLink
		usable := false
		for _, m := range msgs {
			hop.MessageIDs = append(hop.MessageIDs, m.Message.ID)
			if media, ok := tmedia.GetMedia(m.Message); ok && media.Size > 0 {
				usable = true
				key := resourceIdentity(media)
				if !mediaSeen[key] {
					files = append(files, linkedResource{resourceMessage: m, Media: media})
					mediaSeen[key] = true
				}
			}
			next = append(next, messageResourceLinks(m.Message)...)
		}
		if len(next) == 0 && l.Kind == "message" && defaultOn(r.opts.ScanComments) {
			for _, m := range msgs {
				comments, err := r.backend.Comments(ctx, m.Peer, m.Message, r.opts.CommentLimit)
				if err != nil {
					return fmt.Errorf("linked post comments: %w", err)
				}
				for _, comment := range comments {
					next = append(next, messageResourceLinks(comment.Message)...)
				}
			}
		}
		hops = append(hops, hop)
		if !usable && len(next) == 0 {
			return fmt.Errorf("%s returned no files or resource links", l.URL())
		}
		for _, n := range next {
			if err := walk(n, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, l := range roots {
		if err := walk(l, 1); err != nil {
			return files, hops, err
		}
	}
	if len(files) == 0 {
		return nil, hops, fmt.Errorf("no resource files resolved")
	}
	return files, hops, nil
}

func resourceIdentity(m *tmedia.Media) string {
	switch l := m.InputFileLoc.(type) {
	case *tg.InputDocumentFileLocation:
		return fmt.Sprintf("document_%d", l.ID)
	case *tg.InputPhotoFileLocation:
		return fmt.Sprintf("photo_%d_%s", l.ID, l.ThumbSize)
	}
	return ""
}

type telegramLinkBackend struct {
	api         *tg.Client
	manager     *peers.Manager
	opts        LinkOptions
	cleanupMu   sync.Mutex
	cleanupIDs  map[int]bool
	updates     *BotUpdates
	watchedBots map[int64]bool
}

// Remember IDs immediately, including requests whose responses later time out.
func (b *telegramLinkBackend) rememberBotMessages(ids ...int) {
	if !defaultOn(b.opts.CleanupBotMessages) {
		return
	}
	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()
	if b.cleanupIDs == nil {
		b.cleanupIDs = map[int]bool{}
	}
	for _, id := range ids {
		if id > 0 {
			b.cleanupIDs[id] = true
		}
	}
}

func (b *telegramLinkBackend) Cleanup(ctx context.Context) error {
	if b.updates != nil {
		for bot := range b.watchedBots {
			for _, m := range b.updates.Stop(bot) {
				if !m.Out {
					b.rememberBotMessages(m.ID)
				}
			}
			delete(b.watchedBots, bot)
		}
	}
	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()
	ids := make([]int, 0, len(b.cleanupIDs))
	for id := range b.cleanupIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var failures error
	for start := 0; start < len(ids); start += 100 {
		batch := ids[start:min(start+100, len(ids))]
		_, err := b.api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: batch})
		if err != nil {
			failures = multierr.Append(failures, fmt.Errorf("delete bot archive messages %v: %w", batch, err))
			continue
		}
		for _, id := range batch {
			delete(b.cleanupIDs, id)
		}
	}
	return failures
}

type botCooldownError struct{ seconds int }

func (e *botCooldownError) Error() string { return fmt.Sprintf("bot cooldown: %d seconds", e.seconds) }

var botFloodMarker = regexp.MustCompile(`(?i)flood|too many|rate.?limit|retry after|try again in|please wait|频繁|太快|冷却|稍后再试|请.{0,8}等待|间隔.{0,8}(请求|获取)|请求.{0,8}间隔`)
var botFloodDuration = regexp.MustCompile(`(?i)(\d+)\s*(seconds?|secs?|sec|s\b|minutes?|mins?|min|m\b|hours?|hrs?|h\b|秒钟?|分钟?|小时)`)
var botFloodExplicit = regexp.MustCompile(`(?i)flood|too many|rate.?limit|retry|again`)

func botCooldown(text string, fallback int) (int, bool) {
	if !botFloodMarker.MatchString(text) {
		return 0, false
	}
	if strings.Contains(strings.ToLower(text), "automatically") || strings.Contains(text, "自动发送") || strings.Contains(text, "正在处理") {
		return 0, false // progress notifications are not a request to retry
	}
	seconds := 0
	for _, match := range botFloodDuration.FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil || n > 86400 {
			return 86401, true
		}
		unit := strings.ToLower(match[2])
		multiplier := 1
		if strings.HasPrefix(unit, "m") || strings.HasPrefix(unit, "分") {
			multiplier = 60
		}
		if strings.HasPrefix(unit, "h") || strings.HasPrefix(unit, "小时") {
			multiplier = 3600
		}
		seconds = max(seconds, n*multiplier)
	}
	if seconds == 0 {
		if strings.Contains(strings.ToLower(text), "please wait") && !botFloodExplicit.MatchString(text) {
			return 0, false
		}
		seconds = fallback
	}
	return seconds, true
}

func waitLinked(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (b *telegramLinkBackend) Fetch(ctx context.Context, l resourceLink) ([]resourceMessage, error) {
	var peer peers.Peer
	var err error
	if id, parseErr := strconv.ParseInt(l.Chat, 10, 64); parseErr == nil && l.Kind != "bot" {
		// /c/ links identify channels; keep the typed error instead of falling
		// through user/chat resolvers, which would obscure an unavailable group.
		peer, err = b.manager.ResolveChannelID(ctx, id)
	} else {
		peer, err = tutil.GetInputPeer(ctx, b.manager, l.Chat)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve %s peer %q: %w", l.Kind, l.Chat, err)
	}
	if l.Kind == "bot" {
		u, ok := peer.(peers.User)
		if !ok || !u.Raw().Bot {
			return nil, tgerr.New(400, "BOT_INVALID")
		}
		if u.Raw().Deleted {
			return nil, tgerr.New(400, "INPUT_USER_DEACTIVATED")
		}
		return b.requestBot(ctx, u, l.Start)
	}
	if l.Comment > 0 {
		m, err := getLinkedMessage(ctx, b.api, archiveInputPeer(peer), l.ID)
		if err != nil {
			return nil, err
		}
		if l.TopicID > 0 {
			if err := verifyLinkedTopic(ctx, b.api, archiveInputPeer(peer), l.TopicID); err != nil {
				return nil, err
			}
			if !linkedTopicMember(m, l.TopicID) {
				return nil, fmt.Errorf("message %d does not belong to linked forum topic %d", m.ID, l.TopicID)
			}
		}
		discussionPeer, _, err := b.discussion(ctx, archiveInputPeer(peer), m)
		if err != nil {
			return nil, err
		}
		return b.messageAlbum(ctx, discussionPeer, l.Comment, l.Single)
	}
	return b.resourceMessages(ctx, archiveInputPeer(peer), l.ID, l.Single, l.TopicID)
}

func getLinkedMessage(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, id int) (*tg.Message, error) {
	raw, err := getLinkedRawMessage(ctx, api, peer, id)
	if err != nil {
		return nil, err
	}
	if m, ok := raw.(*tg.Message); ok {
		return m, nil
	}
	if m, ok := raw.(*tg.MessageService); ok {
		return nil, unsupportedLinkedService(m)
	}
	return nil, fmt.Errorf("message %d is unavailable or deleted", id)
}

func getLinkedRawMessage(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, id int) (tg.MessageClass, error) {
	if id <= 0 {
		return nil, fmt.Errorf("message ID must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var res tg.MessagesMessagesClass
	var err error
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	if p, ok := peer.(*tg.InputPeerChannel); ok {
		res, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}, ID: ids})
	} else {
		res, err = api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, err
	}
	modified, ok := res.AsModified()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("unexpected message result %T", res)
	}
	for _, raw := range modified.GetMessages() {
		if raw.GetID() == id {
			if err := validateLinkedPeer(peer, raw); err != nil {
				return nil, err
			}
			return raw, nil
		}
	}
	return nil, fmt.Errorf("message %d is unavailable or deleted", id)
}

func (b *telegramLinkBackend) messageAlbum(ctx context.Context, peer tg.InputPeerClass, id int, single bool) ([]resourceMessage, error) {
	m, err := getLinkedMessage(ctx, b.api, peer, id)
	if err != nil {
		return nil, err
	}
	return b.albumFromMessage(ctx, peer, m, single, 0)
}

func (b *telegramLinkBackend) albumFromMessage(ctx context.Context, peer tg.InputPeerClass, m *tg.Message, single bool, topicID int) ([]resourceMessage, error) {
	album := []*tg.Message{m}
	var err error
	if m.GroupedID != 0 && !single {
		album, err = tutil.GetGroupedMessages(ctx, b.api, peer, m)
		if err != nil {
			return nil, err
		}
		if len(album) == 0 {
			return nil, fmt.Errorf("linked album is unavailable")
		}
	}
	result := make([]resourceMessage, 0, len(album))
	for _, m := range album {
		if err := validateLinkedPeer(peer, m); err != nil {
			return nil, err
		}
		if topicID > 0 && !linkedTopicMember(m, topicID) {
			return nil, fmt.Errorf("album message %d does not belong to linked forum topic %d", m.ID, topicID)
		}
		result = append(result, resourceMessage{Peer: peer, Message: m})
	}
	return result, nil
}

func (b *telegramLinkBackend) discussion(ctx context.Context, peer tg.InputPeerClass, m *tg.Message) (tg.InputPeerClass, int, error) {
	res, err := b.api.MessagesGetDiscussionMessage(ctx, &tg.MessagesGetDiscussionMessageRequest{Peer: peer, MsgID: m.ID})
	if err != nil {
		return nil, 0, err
	}
	// Telegram returns the thread's initial messages newest first. The last
	// matching message is the root, including when the post is an album.
	// https://core.telegram.org/api/discussion
	for i := len(res.Messages) - 1; i >= 0; i-- {
		raw := res.Messages[i]
		root, ok := raw.(*tg.Message)
		if !ok {
			continue
		}
		id := tutil.GetPeerID(root.PeerID)
		if id == tutil.GetInputPeerID(peer) {
			continue
		}
		if replies, ok := m.GetReplies(); ok && replies.ChannelID != 0 && id != replies.ChannelID {
			continue
		}
		for _, chat := range res.Chats {
			switch c := chat.(type) {
			case *tg.Channel:
				if c.ID == id {
					return &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash}, root.ID, nil
				}
			case *tg.Chat:
				if c.ID == id {
					return &tg.InputPeerChat{ChatID: c.ID}, root.ID, nil
				}
			}
		}
	}
	return nil, 0, fmt.Errorf("discussion root is unavailable")
}

func (b *telegramLinkBackend) Comments(ctx context.Context, peer tg.InputPeerClass, m *tg.Message, limit int) ([]resourceMessage, error) {
	replies, ok := m.GetReplies()
	if !ok || !replies.Comments || replies.Replies == 0 {
		return nil, nil
	}
	discussionPeer, root, err := b.discussion(ctx, peer, m)
	if err != nil {
		return nil, err
	}
	it := messages.NewIterator(query.NewQuery(b.api).Messages().GetReplies(discussionPeer).MsgID(root), 100)
	var result []resourceMessage
	for len(result) < limit && it.Next(ctx) {
		if msg, ok := it.Value().Msg.(*tg.Message); ok && msg.ID != root {
			result = append(result, resourceMessage{Peer: discussionPeer, Message: msg})
		}
	}
	return result, it.Err()
}

func (b *telegramLinkBackend) historySince(ctx context.Context, peer tg.InputPeerClass, after int) ([]*tg.Message, error) {
	var result []*tg.Message
	responses := 0
	offset := 0
	for {
		res, err := b.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, MinID: after, OffsetID: offset, Limit: 100})
		if err != nil {
			return result, fmt.Errorf("read bot %d response history after %d (offset %d): %w", tutil.GetInputPeerID(peer), after, offset, err)
		}
		modified, ok := res.AsModified()
		if !ok {
			return result, fmt.Errorf("unexpected bot history result")
		}
		raw := modified.GetMessages()
		if len(raw) == 0 {
			break
		}
		last := raw[len(raw)-1].GetID()
		for _, raw := range raw {
			if m, ok := raw.(*tg.Message); ok && m.ID > after {
				result = append(result, m)
				if !m.Out {
					responses++
				}
			}
		}
		if responses > b.opts.MaxBotMessages {
			return result, fmt.Errorf("bot response exceeds max_bot_messages")
		}
		if len(raw) < 100 || last <= after {
			break
		}
		if offset != 0 && last >= offset {
			return result, fmt.Errorf("bot history pagination did not advance")
		}
		offset = last
	}
	return result, nil
}

func updateMessages(u tg.UpdatesClass) []*tg.Message {
	var updates []tg.UpdateClass
	switch u := u.(type) {
	case *tg.Updates:
		updates = u.Updates
	case *tg.UpdatesCombined:
		updates = u.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{u.Update}
	}
	var result []*tg.Message
	for _, u := range updates {
		var raw tg.MessageClass
		switch u := u.(type) {
		case *tg.UpdateNewMessage:
			raw = u.Message
		case *tg.UpdateNewChannelMessage:
			raw = u.Message
		case *tg.UpdateEditMessage:
			raw = u.Message
		}
		if m, ok := raw.(*tg.Message); ok {
			result = append(result, m)
		}
	}
	return result
}

func botRequestID(u tg.UpdatesClass, randomID, botID int64) int {
	if short, ok := u.(*tg.UpdateShortSentMessage); ok {
		return short.ID
	}
	var updates []tg.UpdateClass
	switch u := u.(type) {
	case *tg.Updates:
		updates = u.Updates
	case *tg.UpdatesCombined:
		updates = u.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{u.Update}
	}
	for _, u := range updates {
		if id, ok := u.(*tg.UpdateMessageID); ok && id.RandomID == randomID {
			return id.ID
		}
	}
	for _, m := range updateMessages(u) {
		if m.Out && tutil.GetPeerID(m.PeerID) == botID {
			return m.ID
		}
	}
	return 0
}

func (b *telegramLinkBackend) requestBot(ctx context.Context, bot peers.User, start string) ([]resourceMessage, error) {
	for retry := 0; ; retry++ {
		result, err := b.requestBotOnce(ctx, bot, start)
		var cooldown *botCooldownError
		if !errors.As(err, &cooldown) {
			return result, err
		}
		if retry >= *b.opts.FloodRetries {
			return nil, fmt.Errorf("bot flood_retries exhausted: %w", err)
		}
		if cooldown.seconds > b.opts.MaxFloodWait {
			return nil, fmt.Errorf("bot cooldown exceeds max_flood_wait_seconds: %w", err)
		}
		fmt.Printf("Bot %d rate limited; waiting %d seconds before retry %d/%d\n", bot.ID(), cooldown.seconds, retry+1, *b.opts.FloodRetries)
		if err := waitLinked(ctx, time.Duration(cooldown.seconds)*time.Second); err != nil {
			return nil, err
		}
	}
}

func (b *telegramLinkBackend) requestBotOnce(ctx context.Context, bot peers.User, start string) ([]resourceMessage, error) {
	// The existing Telegram middleware may wait through an RPC FLOOD_WAIT.
	// Start the bot response timer after the start RPC finishes so that this
	// server-required wait is not cut short by bot_timeout_seconds.
	// A fresh watermark excludes old resources belonging to previous posts.
	res, err := b.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: bot.InputPeer(), Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("read bot %d request watermark: %w", bot.ID(), err)
	}
	modified, ok := res.AsModified()
	if !ok {
		return nil, fmt.Errorf("unexpected bot history result")
	}
	watermark := 0
	for _, m := range modified.GetMessages() {
		watermark = max(watermark, m.GetID())
	}
	if b.updates != nil {
		b.updates.Watch(bot.ID(), watermark, (b.opts.MaxBotMessages+1)*(*b.opts.ReRequestLimit+1)*(*b.opts.FloodRetries+1))
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("generate bot %d request ID: %w", bot.ID(), err)
	}
	randomID := int64(binary.LittleEndian.Uint64(random[:]))
	updates, err := b.api.MessagesStartBot(ctx, &tg.MessagesStartBotRequest{Bot: bot.InputUser(), Peer: bot.InputPeer(), RandomID: randomID, StartParam: start})
	if err != nil {
		if b.updates != nil && !b.watchedBots[bot.ID()] {
			b.updates.Stop(bot.ID())
		}
		if seconds, ok := tgerr.AsFloodWait(err); ok {
			return nil, &botCooldownError{seconds: int(seconds/time.Second) + 1}
		}
		return nil, fmt.Errorf("start bot %d: %w", bot.ID(), err)
	}
	responseCtx, cancelResponse := context.WithTimeout(ctx, time.Duration(b.opts.BotTimeout)*time.Second)
	defer cancelResponse()
	requestID := botRequestID(updates, randomID, bot.ID())
	if b.updates != nil {
		if b.watchedBots == nil {
			b.watchedBots = map[int64]bool{}
		}
		b.watchedBots[bot.ID()] = true
	}
	b.rememberBotMessages(requestID)
	seen := map[int]*tg.Message{}
	fingerprints := map[int]string{}
	liveFingerprints := map[int]string{}
	historyFingerprints := map[int]string{}
	lastChange := time.Now()
	generation := int64(0)
	collect := func(msgs []*tg.Message, sourceFingerprints map[int]string) error {
		for _, m := range msgs {
			if m.ID <= watermark || tutil.GetPeerID(m.PeerID) != bot.ID() {
				continue
			}
			if m.Out {
				if m.ID == requestID {
					b.rememberBotMessages(m.ID)
				}
				continue
			}
			if requestID > 0 && m.ID < requestID {
				continue
			}
			b.rememberBotMessages(m.ID)
			data, _ := json.Marshal(m)
			// Snapshots retain old live updates. Consume each source version once
			// so it cannot overwrite newer history metadata on every poll.
			if sourceFingerprints[m.ID] == string(data) {
				continue
			}
			sourceFingerprints[m.ID] = string(data)
			if previous := seen[m.ID]; previous != nil && m.EditDate < previous.EditDate {
				continue
			}
			fingerprint := botMessageFingerprint(m)
			seen[m.ID] = m // keep fresh download references even without a content change
			if fingerprints[m.ID] != fingerprint {
				fingerprints[m.ID] = fingerprint
				lastChange = time.Now()
				generation++
			}
		}
		if len(seen) > b.opts.MaxBotMessages {
			return fmt.Errorf("bot response exceeds max_bot_messages")
		}
		return nil
	}
	if err := collect(updateMessages(updates), liveFingerprints); err != nil {
		return nil, err
	}
	collectLive := func() error {
		if b.updates == nil {
			return nil
		}
		msgs, err := b.updates.Snapshot(bot.ID(), watermark)
		if collectErr := collect(msgs, liveFingerprints); collectErr != nil {
			return collectErr
		}
		return err
	}
	tick := time.NewTicker(time.Duration(b.opts.PollInterval) * time.Millisecond)
	defer tick.Stop()
	historyBase := time.Duration(b.opts.PollInterval) * time.Millisecond
	historyInterval := historyBase
	historyMaximum := max(5*time.Second, historyBase)
	var nextHistory time.Time
	finish := func() []resourceMessage {
		ids := make([]int, 0, len(seen))
		for id := range seen {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		result := make([]resourceMessage, 0, len(ids))
		for _, id := range ids {
			result = append(result, resourceMessage{Peer: bot.InputPeer(), Message: seen[id]})
		}
		return result
	}
	responseError := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if responseCtx.Err() != nil {
			return botResponseTimeout(bot.ID(), b.opts.BotTimeout, seen)
		}
		return err
	}
	for {
		cycleGeneration := generation
		if err := collectLive(); err != nil {
			return nil, err
		}
		if generation != cycleGeneration {
			historyInterval = historyBase
			nextHistory = time.Time{}
		}
		// Poll live updates at the configured frequency, but back off history RPCs
		// when nothing changes. A final history read proves the idle boundary.
		actionable := botMessagesActionable(seen)
		idle := actionable && time.Since(lastChange) >= time.Duration(b.opts.BotIdle)*time.Second
		readHistory := !time.Now().Before(nextHistory) || idle
		if readHistory {
			msgs, err := b.historySince(responseCtx, bot.InputPeer(), watermark)
			if collectErr := collect(msgs, historyFingerprints); collectErr != nil {
				return nil, collectErr
			}
			// Replies can arrive (and self-delete) while the history RPC is in
			// flight. Include them before accepting the final idle boundary.
			if collectErr := collectLive(); collectErr != nil {
				return nil, collectErr
			}
			if err != nil {
				return nil, responseError(err)
			}
			if generation != cycleGeneration {
				historyInterval = historyBase
			} else {
				historyInterval = min(historyMaximum, 2*historyInterval)
			}
			nextHistory = time.Now().Add(historyInterval)
		}
		if err := responseCtx.Err(); err != nil {
			return nil, responseError(err)
		}
		actionable = botMessagesActionable(seen)
		if !actionable {
			for _, m := range seen {
				if seconds, ok := botCooldown(m.Message, b.opts.FloodWait); ok {
					return nil, &botCooldownError{seconds: seconds}
				}
			}
		}
		if actionable && time.Since(lastChange) >= time.Duration(b.opts.BotIdle)*time.Second && readHistory {
			return finish(), nil
		}
		select {
		case <-responseCtx.Done():
			return nil, responseError(responseCtx.Err())
		case <-tick.C:
		}
	}
}

// Compare the reply content used by the resolver. Read flags, preview metadata
// and refreshed file references may differ across updates and history reads;
// none of those differences means the bot is still sending its response.
func botMessageFingerprint(m *tg.Message) string {
	content := struct {
		Text       string
		EditDate   int
		GroupedID  int64
		MediaType  string
		ResourceID string
		FileName   string
		Size       int64
		DC         int
		Links      []string
	}{Text: m.Message, EditDate: m.EditDate, GroupedID: m.GroupedID}
	if m.Media != nil {
		content.MediaType = m.Media.TypeName()
	}
	if media, ok := tmedia.GetMedia(m); ok {
		content.ResourceID = resourceIdentity(media)
		content.FileName, content.Size, content.DC = media.Name, media.Size, media.DC
	}
	for _, link := range messageResourceLinks(m) {
		content.Links = append(content.Links, link.key())
	}
	sort.Strings(content.Links)
	data, _ := json.Marshal(content)
	return string(data)
}

func botMessagesActionable(messages map[int]*tg.Message) bool {
	for _, m := range messages {
		if _, ok := tmedia.GetMedia(m); ok || len(messageResourceLinks(m)) > 0 {
			return true
		}
	}
	return false
}

func botResponseTimeout(botID int64, seconds int, messages map[int]*tg.Message) error {
	reason := "no actionable resource received"
	if botMessagesActionable(messages) {
		reason = "resource replies did not settle"
	}
	lastID := 0
	summary := ""
	for id, m := range messages {
		if id <= lastID {
			continue
		}
		lastID = id
		summary = strings.Join(strings.Fields(m.Message), " ")
		if summary == "" && m.Media != nil {
			summary = m.Media.TypeName()
		}
	}
	runes := []rune(summary)
	if len(runes) > 160 {
		summary = string(runes[:159]) + "…"
	}
	if summary == "" {
		summary = "no reply content"
	}
	return fmt.Errorf("bot %d response did not settle within %d seconds (%d messages): %s; last reply (ID %d): %q; adjust bot_timeout_seconds/bot_idle_seconds: %w", botID, seconds, len(messages), reason, lastID, summary, context.DeadlineExceeded)
}
