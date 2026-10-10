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
	"unicode"
	"unicode/utf16"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tgref"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/console"
	uimessages "github.com/iyear/tdl/pkg/messages"
)

// LinkOptions controls bounded resolution of resources linked from a post.
type LinkOptions struct {
	MaxDepth           int   `json:"max_depth" yaml:"max_depth"`
	MaxLinks           int   `json:"max_links" yaml:"max_links"`
	BotTimeout         int   `json:"bot_timeout_seconds" yaml:"bot_timeout_seconds"`
	BotIdle            int   `json:"bot_idle_seconds" yaml:"bot_idle_seconds"`
	BotRequestInterval int   `json:"bot_request_interval_seconds" yaml:"bot_request_interval_seconds"`
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
	BotTextIdle        int   `json:"bot_text_idle_seconds" yaml:"bot_text_idle_seconds"`
	FollowSeries       *bool `json:"follow_series" yaml:"follow_series"`
	SeriesGap          int   `json:"series_gap_seconds" yaml:"series_gap_seconds"`
	DeferPromotions    *bool `json:"defer_promotions" yaml:"defer_promotions"`
	// Additional case-insensitive substrings that mark a link label as promotion.
	PromotionKeywords []string `json:"promotion_keywords" yaml:"promotion_keywords"`
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
		{"bot_request_interval_seconds", &o.BotRequestInterval, 0, 86400},
		{"poll_interval_ms", &o.PollInterval, 500, 60000},
		{"max_bot_messages", &o.MaxBotMessages, 500, 10000},
		{"max_topic_messages", &o.MaxTopicMessages, 1000, 100000},
		{"comment_limit", &o.CommentLimit, 100, 10000},
		{"flood_wait_seconds", &o.FloodWait, 30, 3600},
		{"max_flood_wait_seconds", &o.MaxFloodWait, 3600, 86400},
		{"bot_text_idle_seconds", &o.BotTextIdle, 10, 3600},
		{"series_gap_seconds", &o.SeriesGap, 120, 3600},
	} {
		if *field.value < 0 || *field.value > field.max {
			return diagnostic.Describe(fmt.Errorf("%s must be between 0 and %d", field.name, field.max), corei18n.Message{ID: "errors.message.value_must_be_between_0_and_value", Args: map[string]any{"Arg1": field.name, "Arg2": field.max}})
		}
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	if o.BotIdle >= o.BotTimeout {
		return diagnostic.Describe(fmt.Errorf("bot_idle_seconds must be less than bot_timeout_seconds"), corei18n.Message{ID: "errors.message.bot_key_idle_key_seconds_must_be_less_than_bot_key_timeout_key_seconds"})
	}
	if o.PollInterval >= o.BotTimeout*1000 {
		return diagnostic.Describe(fmt.Errorf("poll_interval_ms must be less than bot_timeout_seconds"), corei18n.Message{ID: "errors.message.poll_key_interval_key_ms_must_be_less_than_bot_key_timeout_key_seconds"})
	}
	if o.ReRequestLimit == nil {
		v := 3
		o.ReRequestLimit = &v
	}
	if *o.ReRequestLimit < 0 || *o.ReRequestLimit > 10 {
		return diagnostic.Describe(fmt.Errorf("rerequest_limit must be between 0 and 10"), corei18n.Message{ID: "errors.message.rerequest_key_limit_must_be_between_0_and_10"})
	}
	if o.FloodRetries == nil {
		v := 5
		o.FloodRetries = &v
	}
	if *o.FloodRetries < 0 || *o.FloodRetries > 20 {
		return diagnostic.Describe(fmt.Errorf("flood_retries must be between 0 and 20"), corei18n.Message{ID: "errors.message.flood_key_retries_must_be_between_0_and_20"})
	}
	if o.FloodWait > o.MaxFloodWait {
		return diagnostic.Describe(fmt.Errorf("flood_wait_seconds must not exceed max_flood_wait_seconds"), corei18n.Message{ID: "errors.bot.flood_wait_limit"})
	}
	keywords := make([]string, 0, len(o.PromotionKeywords))
	for _, k := range o.PromotionKeywords {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			keywords = append(keywords, k)
		}
	}
	o.PromotionKeywords = keywords
	return nil
}

// Labels are button texts, link anchor texts and the caption text around plain
// URLs. Bot names and start parameters are never used as promotion evidence.
var promotionLabel = regexp.MustCompile(`(?i)推广|广告|赞助|金主|商务|合作|互推|招商|投放|sponsor|promo|advert|\bads?\b`)

// promotion reports whether a link label marks a promotion. It returns false
// when deferral is disabled, so every link is then resolved in order.
func (o LinkOptions) promotion(label string) bool {
	if !defaultOn(o.DeferPromotions) || strings.TrimSpace(label) == "" {
		return false
	}
	if promotionLabel.MatchString(label) {
		return true
	}
	label = strings.ToLower(label)
	for _, k := range o.PromotionKeywords {
		if strings.Contains(label, k) {
			return true
		}
	}
	return false
}

func defaultOn(v *bool) bool { return v == nil || *v }

// Resource link kinds. The values are part of persisted link fingerprints.
const (
	linkKindChat    = "chat"
	linkKindMessage = "message"
	linkKindBot     = "bot"
)

type resourceLink struct {
	Kind    string
	Chat    string
	ID      int
	TopicID int
	Start   string
	Comment int
	Single  bool
	// Promo is resolution scheduling only; it is not part of key().
	Promo bool
}

func (l resourceLink) key() string {
	return fmt.Sprintf("%s:%s:%d:%d:%s:%d:%t", l.Kind, strings.ToLower(l.Chat), l.ID, l.TopicID, l.Start, l.Comment, l.Single)
}

func (l resourceLink) URL() string {
	if l.Kind == linkKindBot {
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

// Only message links and bot start links are actionable. Home, invite, payment,
// share and external web links do not identify a resource for this archive.
func parseResourceLink(raw string) (resourceLink, error) {
	ref, err := tgref.Parse(raw, tgref.Options{AllowTG: true})
	if err != nil {
		return resourceLink{}, err
	}
	kind := linkKindChat
	if ref.Bot {
		kind = linkKindBot
	} else if ref.MessageID > 0 {
		kind = linkKindMessage
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

// utf16ByteOffset converts a Telegram entity offset to a byte offset in s.
func utf16ByteOffset(s string, offset int) int {
	units := 0
	for i, r := range s {
		if units >= offset {
			return i
		}
		units += utf16.RuneLen(r)
	}
	return len(s)
}

type linkOccurrence struct {
	raw        string
	start, end int // byte range in the message text; -1 for buttons
	label      string
}

// linkOccurrences lists entity URLs, URL buttons and plain URLs, in that order.
// A text link is labeled by its anchor and the text between it and the previous
// link on its line, plus the rest of the line when no other link follows it
// there. A link alone on its line takes the preceding line as its label (for
// "Ad 👇" above it) when that line holds no link of its own.
func linkOccurrences(m *tg.Message) []linkOccurrence {
	s := m.Message
	var text []linkOccurrence
	for _, e := range m.Entities {
		var raw, anchor string
		start, end := utf16ByteOffset(s, e.GetOffset()), utf16ByteOffset(s, e.GetOffset()+e.GetLength())
		end = max(start, end)
		switch e := e.(type) {
		case *tg.MessageEntityTextURL:
			raw, anchor = e.URL, s[start:end]
		case *tg.MessageEntityURL:
			raw = utf16Text(s, e.Offset, e.Length)
		default:
			continue
		}
		text = append(text, linkOccurrence{raw: raw, start: start, end: end, label: anchor})
	}
	entities := len(text)
	for _, loc := range telegramURL.FindAllStringIndex(s, -1) {
		text = append(text, linkOccurrence{raw: s[loc[0]:loc[1]], start: loc[0], end: loc[1]})
	}
	byStart := make([]int, len(text))
	for i := range byStart {
		byStart[i] = i
	}
	sort.SliceStable(byStart, func(i, j int) bool { return text[byStart[i]].start < text[byStart[j]].start })
	hasLink := func(from, to int) bool {
		for _, o := range text {
			if o.start >= from && o.start < to {
				return true
			}
		}
		return false
	}
	labels := make([]string, len(text))
	for k, i := range byStart {
		o := text[i]
		lineStart := strings.LastIndexByte(s[:o.start], '\n') + 1
		before := lineStart
		for j := k - 1; j >= 0; j-- {
			if prev := text[byStart[j]]; prev.end <= o.start {
				before = max(before, prev.end)
				break
			}
		}
		lineEnd := len(s)
		if n := strings.IndexByte(s[o.end:], '\n'); n >= 0 {
			lineEnd = o.end + n
		}
		after := ""
		next := k + 1
		for next < len(byStart) && text[byStart[next]].start < o.end {
			next++
		}
		if next == len(byStart) || text[byStart[next]].start >= lineEnd {
			after = s[o.end:lineEnd]
		}
		context := s[before:o.start] + " " + after
		if !strings.ContainsFunc(context, unicode.IsLetter) && lineStart > 0 {
			prevStart := strings.LastIndexByte(s[:lineStart-1], '\n') + 1
			if !hasLink(prevStart, lineStart) {
				context += " " + s[prevStart:lineStart-1]
			}
		}
		labels[i] = o.label + " " + context
	}
	for i := range text {
		text[i].label = labels[i]
	}
	var buttons []linkOccurrence
	if keyboard, ok := m.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
		for _, row := range keyboard.Rows {
			for _, b := range row.Buttons {
				if b, ok := b.(*tg.KeyboardButtonURL); ok {
					buttons = append(buttons, linkOccurrence{raw: b.URL, start: -1, end: -1, label: b.Text})
				}
			}
		}
	}
	return append(append(text[:entities:entities], buttons...), text[entities:]...)
}

// messageResourceLinks returns actionable links in their first appearance
// order. A promotion label on any link to a target marks every link to that
// target in the same message, since ads repeat one bot under several labels.
func messageResourceLinks(m *tg.Message, promotion func(string) bool) []resourceLink {
	seen := map[string]bool{}
	promoted := map[string]bool{}
	var links []resourceLink
	for _, o := range linkOccurrences(m) {
		l, err := parseResourceLink(strings.TrimRight(o.raw, ".,;!，。；！)]）】"))
		if err != nil || l.Kind == linkKindChat {
			continue
		}
		if promotion != nil && promotion(o.label) {
			promoted[strings.ToLower(l.Chat)] = true
		}
		if seen[l.key()] {
			continue
		}
		seen[l.key()] = true
		links = append(links, l)
	}
	for i := range links {
		links[i].Promo = promoted[strings.ToLower(links[i].Chat)]
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
	Skipped    string `json:"skipped,omitempty"`
}

type linkResolver struct {
	backend     linkBackend
	opts        LinkOptions
	unavailable *UnavailableLinks
}

// promotionMedia reports a standalone photo whose links are all promotions:
// an ad banner. Albums cannot carry buttons, and documents or videos with an
// ad button attached are often the resource itself, so neither qualifies.
func promotionMedia(m *tg.Message, links []resourceLink) bool {
	if _, ok := m.Media.(*tg.MessageMediaPhoto); !ok || m.GroupedID != 0 || len(links) == 0 {
		return false
	}
	for _, l := range links {
		if !l.Promo {
			return false
		}
	}
	return true
}

type deferredLink struct {
	link  resourceLink
	depth int
}

func (r *linkResolver) Resolve(ctx context.Context, roots []resourceLink) ([]linkedResource, []linkHop, error) {
	if r.unavailable == nil {
		r.unavailable = &UnavailableLinks{}
	}
	var files, promoFiles []linkedResource
	var hops []linkHop
	var deadEnds error
	var deferred []deferredLink
	done, active, mediaSeen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	// A bot response can contain both resources and unrelated promotion links.
	// Explore every other branch first, retaining media from every hop. A dead
	// end must not discard earlier media or prevent a later sibling from
	// supplying files. Links labeled as promotions are requested only when no
	// other branch produced a file.
	stop := func(hop linkHop, err error) error {
		hop.Skipped = err.Error()
		hops = append(hops, hop)
		deadEnds = multierr.Append(deadEnds, err)
		return nil
	}
	var walk func(resourceLink, int) error
	follow := func(links []resourceLink, depth int) error {
		for _, n := range links {
			if n.Promo {
				deferred = append(deferred, deferredLink{link: n, depth: depth})
				continue
			}
			if err := walk(n, depth); err != nil {
				return err
			}
		}
		return nil
	}
	walk = func(l resourceLink, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		hop := linkHop{URL: l.URL(), Depth: depth}
		if active[l.key()] {
			return stop(hop, diagnostic.Describe(fmt.Errorf("resource link cycle at %s", l.URL()), corei18n.Message{ID: "errors.message.resource_link_cycle_at_value", Args: map[string]any{"Arg1": l.URL()}}))
		}
		if done[l.key()] {
			return nil
		}
		if depth > r.opts.MaxDepth {
			return stop(hop, diagnostic.Describe(fmt.Errorf("resource link depth exceeds %d", r.opts.MaxDepth), corei18n.Message{ID: "errors.message.resource_link_depth_exceeds_value", Args: map[string]any{"Arg1": r.opts.MaxDepth}}))
		}
		if len(done) >= r.opts.MaxLinks {
			return stop(hop, diagnostic.Describe(fmt.Errorf("resource link count exceeds %d", r.opts.MaxLinks), corei18n.Message{ID: "errors.message.resource_link_count_exceeds_value", Args: map[string]any{"Arg1": r.opts.MaxLinks}}))
		}
		done[l.key()], active[l.key()] = true, true
		defer delete(active, l.key())
		if err := r.unavailable.lookup(l); err != nil {
			return stop(hop, err)
		}
		msgs, err := r.backend.Fetch(ctx, l)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			cause := r.unavailable.rememberContext(ctx, l, err)
			err = diagnostic.Describe(fmt.Errorf("resolve %s: %w", l.URL(), cause), corei18n.Message{ID: "errors.message.resolve_value_value", Args: map[string]any{"Arg1": l.URL(), "Arg2": cause}})
			var noReplies *botNoResourceError
			var notBot *notBotTargetError
			if isUnavailableResource(err) || errors.As(err, &noReplies) || errors.As(err, &notBot) {
				return stop(hop, err)
			}
			return err
		}
		var next []resourceLink
		usable := false
		for _, m := range msgs {
			hop.MessageIDs = append(hop.MessageIDs, m.Message.ID)
			links := messageResourceLinks(m.Message, r.opts.promotion)
			if media, ok := tmedia.GetMedia(m.Message); ok && media.Size > 0 {
				usable = true
				key := resourceIdentity(media)
				if !mediaSeen[key] {
					resource := linkedResource{resourceMessage: m, Media: media}
					if promotionMedia(m.Message, links) {
						promoFiles = append(promoFiles, resource)
					} else {
						files = append(files, resource)
					}
					mediaSeen[key] = true
				}
			}
			next = append(next, links...)
		}
		if len(next) == 0 && l.Kind == linkKindMessage && defaultOn(r.opts.ScanComments) {
			for _, m := range msgs {
				comments, err := r.backend.Comments(ctx, m.Peer, m.Message, r.opts.CommentLimit)
				if err != nil {
					return diagnostic.Describe(fmt.Errorf("linked post comments: %w", err), corei18n.Message{ID: "errors.message.linked_post_comments_value", Args: map[string]any{"Arg1": err}})
				}
				for _, comment := range comments {
					next = append(next, messageResourceLinks(comment.Message, r.opts.promotion)...)
				}
			}
		}
		if !usable && len(next) == 0 {
			return stop(hop, diagnostic.Describe(fmt.Errorf("%s returned no files or resource links", l.URL()), corei18n.Message{ID: "errors.message.value_returned_no_files_or_resource_links", Args: map[string]any{"Arg1": l.URL()}}))
		}
		hops = append(hops, hop)
		return follow(next, depth+1)
	}
	if err := follow(roots, 1); err != nil {
		return files, hops, err
	}
	// Without files from other branches, request every promotion deferred so
	// far: a label between two links can belong to either of them. Promotions
	// those requests defer in turn (ad chains) only run while no file exists.
	skipped := map[string]bool{}
	fallback := len(files) == 0
	initial := len(deferred)
	for i := 0; i < len(deferred); i++ {
		d := deferred[i]
		if (fallback && i < initial) || len(files) == 0 {
			if err := walk(d.link, d.depth); err != nil {
				return files, hops, err
			}
			continue
		}
		if done[d.link.key()] || skipped[d.link.key()] {
			continue
		}
		skipped[d.link.key()] = true
		reason := diagnostic.Describe(fmt.Errorf("promotion link not requested: resources were found in other branches"), corei18n.Message{ID: "errors.linked.promotion_not_requested"})
		hops = append(hops, linkHop{URL: d.link.URL(), Depth: d.depth, Skipped: reason.Error()})
	}
	if err := ctx.Err(); err != nil {
		return files, hops, err
	}
	if len(files) == 0 {
		files = promoFiles
	} else if len(promoFiles) > 0 {
		fmt.Println(console.Translate(ctx, uimessages.LinkedPromotionMediaSkipped(len(promoFiles))))
	}
	if len(files) == 0 {
		if deadEnds != nil {
			return nil, hops, deadEnds
		}
		return nil, hops, diagnostic.Describe(fmt.Errorf("no resource files resolved"), corei18n.Message{ID: "errors.message.no_resource_files_resolved"})
	}
	for _, hop := range hops {
		if hop.Skipped != "" {
			fmt.Println(console.Translate(ctx, uimessages.LinkedBranchSkipped(hop.URL, len(files), hop.Skipped)))
		}
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
	replyTimes  BotUpdates
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
			failures = multierr.Append(failures, diagnostic.Describe(fmt.Errorf("delete bot archive messages %v: %w", batch, err), corei18n.Message{ID: "errors.message.delete_bot_archive_messages_value_value", Args: map[string]any{"Arg1": batch, "Arg2": err}}))
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

var (
	botFloodMarker   = regexp.MustCompile(`(?i)flood|too many|rate.?limit|retry after|try again in|please wait|频繁|太快|冷却|稍后再试|请.{0,8}等待|间隔.{0,8}(请求|获取)|请求.{0,8}间隔`)
	botFloodDuration = regexp.MustCompile(`(?i)(\d+)\s*(seconds?|secs?|sec|s\b|minutes?|mins?|min|m\b|hours?|hrs?|h\b|秒钟?|分钟?|小时)`)
	botFloodExplicit = regexp.MustCompile(`(?i)flood|too many|rate.?limit|retry|again`)
)

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
	if id, parseErr := strconv.ParseInt(l.Chat, 10, 64); parseErr == nil && l.Kind != linkKindBot {
		// /c/ links identify channels; keep the typed error instead of falling
		// through user/chat resolvers, which would obscure an unavailable group.
		peer, err = b.manager.ResolveChannelID(ctx, id)
	} else {
		peer, err = tutil.GetInputPeer(ctx, b.manager, l.Chat)
	}
	if err != nil {
		return nil, diagnostic.Describe(fmt.Errorf("resolve %s peer %q: %w", l.Kind, l.Chat, err), corei18n.Message{ID: "errors.message.resolve_value_peer_value_value", Args: map[string]any{"Arg1": l.Kind, "Arg2": fmt.Sprintf("%q", l.Chat), "Arg3": err}})
	}
	if l.Kind == linkKindBot {
		u, ok := peer.(peers.User)
		if ok && u.Raw().Deleted {
			return nil, tgerr.New(400, "INPUT_USER_DEACTIVATED")
		}
		if !ok || !u.Raw().Bot {
			return nil, notBotTarget(l, peer)
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
				return nil, diagnostic.Describe(fmt.Errorf("message %d does not belong to linked forum topic %d", m.ID, l.TopicID), corei18n.Message{ID: "errors.message.message_value_does_not_belong_to_linked_forum_topic_value", Args: map[string]any{"Arg1": m.ID, "Arg2": l.TopicID}})
			}
		}
		discussionPeer, root, err := b.discussion(ctx, archiveInputPeer(peer), m)
		if err != nil {
			return nil, err
		}
		album, err := b.messageAlbum(ctx, discussionPeer, l.Comment, l.Single)
		if err != nil || l.Single {
			return album, err
		}
		return b.series(ctx, discussionPeer, album, func(m *tg.Message) bool { return threadMember(m, root) })
	}
	return b.resourceMessages(ctx, archiveInputPeer(peer), l.ID, l.Single, l.TopicID)
}

// A start parameter on a group, channel or person cannot be requested. Report
// the actual target type rather than BOT_INVALID, which would also mark the
// whole chat unavailable and skip its message links for the rest of the run.
type notBotTargetError struct{ err error }

func (e *notBotTargetError) Error() string { return e.err.Error() }
func (e *notBotTargetError) Unwrap() error { return e.err }

func notBotTarget(l resourceLink, peer peers.Peer) error {
	kind, message := "user", corei18n.Message{ID: "linked.target.user", Default: "user"}
	switch p := peer.(type) {
	case peers.Channel:
		kind, message = "channel", corei18n.Message{ID: "linked.target.channel", Default: "channel"}
		if !p.IsBroadcast() {
			kind, message = "group", corei18n.Message{ID: "linked.target.group", Default: "group"}
		}
	case peers.Chat:
		kind, message = "group", corei18n.Message{ID: "linked.target.group", Default: "group"}
	}
	err := diagnostic.Describe(fmt.Errorf("%s points to %s %s (ID %d), not a bot; start parameter %q ignored", l.URL(), kind, l.Chat, peer.ID(), l.Start), corei18n.Message{ID: "errors.linked.not_bot", Args: map[string]any{"Arg1": l.URL(), "Arg2": message, "Arg3": l.Chat, "Arg4": peer.ID(), "Arg5": fmt.Sprintf("%q", l.Start)}})
	return &notBotTargetError{err: err}
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
	return nil, diagnostic.Describe(fmt.Errorf("message %d is unavailable or deleted", id), corei18n.Message{ID: "errors.message.message_value_is_unavailable_or_deleted", Args: map[string]any{"Arg1": id}})
}

func getLinkedRawMessage(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, id int) (tg.MessageClass, error) {
	if id <= 0 {
		return nil, diagnostic.Describe(fmt.Errorf("message ID must be positive"), corei18n.Message{ID: "errors.message.message_id_must_be_positive"})
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
		return nil, diagnostic.Describe(fmt.Errorf("unexpected message result %T", res), corei18n.Message{ID: "errors.message.unexpected_message_result_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", res)}})
	}
	for _, raw := range modified.GetMessages() {
		if raw.GetID() == id {
			if err := validateLinkedPeer(peer, raw); err != nil {
				return nil, err
			}
			return raw, nil
		}
	}
	return nil, diagnostic.Describe(fmt.Errorf("message %d is unavailable or deleted", id), corei18n.Message{ID: "errors.message.message_value_is_unavailable_or_deleted", Args: map[string]any{"Arg1": id}})
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
			return nil, diagnostic.Describe(fmt.Errorf("linked album is unavailable"), corei18n.Message{ID: "errors.message.linked_album_is_unavailable"})
		}
	}
	result := make([]resourceMessage, 0, len(album))
	for _, m := range album {
		if err := validateLinkedPeer(peer, m); err != nil {
			return nil, err
		}
		if topicID > 0 && !linkedTopicMember(m, topicID) {
			return nil, diagnostic.Describe(fmt.Errorf("album message %d does not belong to linked forum topic %d", m.ID, topicID), corei18n.Message{ID: "errors.message.album_message_value_does_not_belong_to_linked_forum_topic_value", Args: map[string]any{"Arg1": m.ID, "Arg2": topicID}})
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
	return nil, 0, diagnostic.Describe(fmt.Errorf("discussion root is unavailable"), corei18n.Message{ID: "errors.message.discussion_root_is_unavailable"})
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
			return result, diagnostic.Describe(fmt.Errorf("read bot %d response history after %d (offset %d): %w", tutil.GetInputPeerID(peer), after, offset, err), corei18n.Message{ID: "errors.message.read_bot_value_response_history_after_value_offset_value_value", Args: map[string]any{"Arg1": tutil.GetInputPeerID(peer), "Arg2": after, "Arg3": offset, "Arg4": err}})
		}
		modified, ok := res.AsModified()
		if !ok {
			return result, diagnostic.Describe(fmt.Errorf("unexpected bot history result"), corei18n.Message{ID: "errors.message.unexpected_bot_history_result"})
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
			return result, diagnostic.Describe(fmt.Errorf("bot response exceeds max_bot_messages"), corei18n.Message{ID: "errors.message.bot_response_exceeds_max_key_bot_key_messages"})
		}
		if len(raw) < 100 || last <= after {
			break
		}
		if offset != 0 && last >= offset {
			return result, diagnostic.Describe(fmt.Errorf("bot history pagination did not advance"), corei18n.Message{ID: "errors.message.bot_history_pagination_did_not_advance"})
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
			return nil, diagnostic.Describe(fmt.Errorf("bot flood_retries exhausted: %w", err), corei18n.Message{ID: "errors.message.bot_flood_key_retries_exhausted_value", Args: map[string]any{"Arg1": err}})
		}
		if cooldown.seconds > b.opts.MaxFloodWait {
			return nil, diagnostic.Describe(fmt.Errorf("bot cooldown exceeds max_flood_wait_seconds: %w", err), corei18n.Message{ID: "errors.message.bot_cooldown_exceeds_max_key_flood_key_wait_key_seconds_value", Args: map[string]any{"Arg1": err}})
		}
		fmt.Println(console.Translate(ctx, uimessages.LinkedBotRateLimit(bot.ID(), cooldown.seconds, retry+1, *b.opts.FloodRetries)))
		if err := waitLinked(ctx, time.Duration(cooldown.seconds)*time.Second); err != nil {
			return nil, err
		}
	}
}

func (b *telegramLinkBackend) botReplyTimes() *BotUpdates {
	if b.updates != nil {
		return b.updates // shared across posts and jobs in this batch
	}
	return &b.replyTimes
}

func (b *telegramLinkBackend) waitBotRequestInterval(ctx context.Context, bot peers.User) error {
	botID := bot.ID()
	interval := time.Duration(b.opts.BotRequestInterval) * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		last := b.botReplyTimes().lastBotReply(botID)
		if interval == 0 || last.sentAt.IsZero() {
			return nil
		}
		remaining := time.Until(last.sentAt.Add(interval))
		if remaining <= 0 {
			return nil
		}
		fmt.Println(console.Translate(ctx, uimessages.LinkedBotInterval(botID, remaining.Seconds())))
		if err := waitLinked(ctx, remaining); err != nil {
			return err
		}
		if b.updates == nil {
			// Without a live handler, check for late replies before accepting
			// the interval boundary. Repeated history copies keep their date.
			res, err := b.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: bot.InputPeer(), Limit: 100})
			if err != nil {
				return diagnostic.Describe(fmt.Errorf("check bot %d replies during request interval: %w", botID, err), corei18n.Message{ID: "errors.message.check_bot_value_replies_during_request_interval_value", Args: map[string]any{"Arg1": botID, "Arg2": err}})
			}
			modified, ok := res.AsModified()
			if !ok {
				return diagnostic.Describe(fmt.Errorf("unexpected bot history result"), corei18n.Message{ID: "errors.message.unexpected_bot_history_result"})
			}
			for _, raw := range modified.GetMessages() {
				if m, ok := raw.(*tg.Message); ok && tutil.GetPeerID(m.PeerID) == botID {
					b.botReplyTimes().recordBotReply(botID, m)
				}
			}
		}
		// A late reply during the wait moves the next request boundary forward.
	}
}

func (b *telegramLinkBackend) requestBotOnce(ctx context.Context, bot peers.User, start string) ([]resourceMessage, error) {
	// The existing Telegram middleware may wait through an RPC FLOOD_WAIT.
	// Start the bot response timer after the start RPC finishes so that this
	// server-required wait is not cut short by bot_timeout_seconds.
	// A fresh watermark excludes old resources belonging to previous posts.
	watermarkLimit := 1
	if b.opts.BotRequestInterval > 0 {
		// The latest message may be our own /start; find the latest incoming
		// reply as well when seeding the request interval from this dialog.
		watermarkLimit = 100
	}
	res, err := b.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: bot.InputPeer(), Limit: watermarkLimit})
	if err != nil {
		return nil, diagnostic.Describe(fmt.Errorf("read bot %d request watermark: %w", bot.ID(), err), corei18n.Message{ID: "errors.message.read_bot_value_request_watermark_value", Args: map[string]any{"Arg1": bot.ID(), "Arg2": err}})
	}
	modified, ok := res.AsModified()
	if !ok {
		return nil, diagnostic.Describe(fmt.Errorf("unexpected bot history result"), corei18n.Message{ID: "errors.message.unexpected_bot_history_result"})
	}
	watermark := 0
	for _, m := range modified.GetMessages() {
		watermark = max(watermark, m.GetID())
		if m, ok := m.(*tg.Message); ok && tutil.GetPeerID(m.PeerID) == bot.ID() {
			b.botReplyTimes().recordBotReply(bot.ID(), m)
		}
	}
	if b.updates != nil {
		b.updates.Watch(bot.ID(), watermark, (b.opts.MaxBotMessages+1)*(*b.opts.ReRequestLimit+1)*(*b.opts.FloodRetries+1))
	}
	if err := b.waitBotRequestInterval(ctx, bot); err != nil {
		if b.updates != nil && !b.watchedBots[bot.ID()] {
			b.updates.Stop(bot.ID())
		}
		return nil, diagnostic.Describe(fmt.Errorf("wait bot %d request interval: %w", bot.ID(), err), corei18n.Message{ID: "errors.message.wait_bot_value_request_interval_value", Args: map[string]any{"Arg1": bot.ID(), "Arg2": err}})
	}
	if b.opts.BotRequestInterval > 0 {
		// Exclude previous-request replies that arrived while waiting, even
		// when the start RPC does not return a request message ID.
		watermark = max(watermark, b.botReplyTimes().lastBotReply(bot.ID()).messageID)
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, diagnostic.Describe(fmt.Errorf("generate bot %d request ID: %w", bot.ID(), err), corei18n.Message{ID: "errors.message.generate_bot_value_request_id_value", Args: map[string]any{"Arg1": bot.ID(), "Arg2": err}})
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
		return nil, diagnostic.Describe(fmt.Errorf("start bot %d: %w", bot.ID(), err), corei18n.Message{ID: "errors.message.start_bot_value_value", Args: map[string]any{"Arg1": bot.ID(), "Arg2": err}})
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
			b.botReplyTimes().recordBotReply(bot.ID(), m)
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
			return diagnostic.Describe(fmt.Errorf("bot response exceeds max_bot_messages"), corei18n.Message{ID: "errors.message.bot_response_exceeds_max_key_bot_key_messages"})
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
		idle := botRepliesIdle(seen, lastChange, b.opts)
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
		actionable := botMessagesActionable(seen)
		if !actionable {
			for _, m := range seen {
				if seconds, ok := botCooldown(m.Message, b.opts.FloodWait); ok {
					return nil, &botCooldownError{seconds: seconds}
				}
			}
		}
		if readHistory && botRepliesIdle(seen, lastChange, b.opts) {
			if !actionable {
				return nil, botTextOnly(bot.ID(), b.opts.BotTextIdle, seen)
			}
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
	for _, link := range messageResourceLinks(m, nil) {
		content.Links = append(content.Links, link.key())
	}
	sort.Strings(content.Links)
	data, _ := json.Marshal(content)
	return string(data)
}

func botMessagesActionable(messages map[int]*tg.Message) bool {
	for _, m := range messages {
		if _, ok := tmedia.GetMedia(m); ok || len(messageResourceLinks(m, nil)) > 0 {
			return true
		}
	}
	return false
}

// Progress and auto-delete notices announce files that are still on their way.
var botProgressMarker = regexp.MustCompile(`(?i)processing|please wait|wait a (moment|minute|second)|uploading|preparing|generating|sending|will be sent|in queue|queued|auto.?delete|正在|处理中|请稍|稍等|稍候|上传中|生成中|准备中|发送中|获取中|即将|马上|排队|加载中|检测到|自动删除|共\s*\d+\s*个`)

// botRepliesIdle reports whether the replies settled. Files and links settle
// after bot_idle_seconds. Text without either (a welcome, an ad or a "join
// first" notice) settles after bot_text_idle_seconds instead of the full
// timeout, unless it announces progress. No reply at all waits for the timeout.
func botRepliesIdle(messages map[int]*tg.Message, lastChange time.Time, opts LinkOptions) bool {
	if len(messages) == 0 {
		return false
	}
	quiet := time.Since(lastChange)
	if botMessagesActionable(messages) {
		return quiet >= time.Duration(opts.BotIdle)*time.Second
	}
	for _, m := range messages {
		if botProgressMarker.MatchString(m.Message) {
			return false
		}
	}
	textIdle := opts.BotTextIdle
	if textIdle <= 0 {
		textIdle = 10 // options that bypassed Normalize
	}
	return quiet >= time.Duration(textIdle)*time.Second
}

func botTextOnly(botID int64, seconds int, messages map[int]*tg.Message) error {
	lastID, summary, summaryMessage := botLastReply(messages)
	err := diagnostic.Describe(fmt.Errorf("bot %d replied without files or resource links and stayed idle for %d seconds (%d messages); last reply (ID %d): %q; adjust bot_text_idle_seconds", botID, seconds, len(messages), lastID, summary), corei18n.Message{ID: "errors.bot.text_only", Args: map[string]any{"Arg1": botID, "Arg2": seconds, "Arg3": len(messages), "Arg4": lastID, "Arg5": summaryMessage}})
	return &botNoResourceError{err: err}
}

// botLastReply summarizes the newest reply for diagnostics.
func botLastReply(messages map[int]*tg.Message) (int, string, any) {
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
	var summaryMessage any = fmt.Sprintf("%q", summary)
	if summary == "" {
		summary = "no reply content"
		summaryMessage = corei18n.Message{ID: "bot.timeout.no_content", Default: "\"no reply content\""}
	}
	return lastID, summary, summaryMessage
}

// A bot's own response deadline with no actionable replies is a dead end.
// Keep it distinct from caller cancellation and resources that did not settle.
type botNoResourceError struct{ err error }

func (e *botNoResourceError) Error() string { return e.err.Error() }
func (e *botNoResourceError) Unwrap() error { return e.err }

func botResponseTimeout(botID int64, seconds int, messages map[int]*tg.Message) error {
	reason := "no actionable resource received"
	reasonMessage := corei18n.Message{ID: "bot.timeout.no_actionable", Default: "no actionable resource received"}
	actionable := botMessagesActionable(messages)
	if actionable {
		reason = "resource replies did not settle"
		reasonMessage = corei18n.Message{ID: "bot.timeout.unsettled", Default: "resource replies did not settle"}
	}
	lastID, summary, summaryMessage := botLastReply(messages)
	err := diagnostic.Describe(fmt.Errorf("bot %d response did not settle within %d seconds (%d messages): %s; last reply (ID %d): %q; adjust bot_timeout_seconds/bot_idle_seconds: %w", botID, seconds, len(messages), reason, lastID, summary, context.DeadlineExceeded), corei18n.Message{ID: "errors.bot.response_timeout", Args: map[string]any{"Arg1": botID, "Arg2": seconds, "Arg3": len(messages), "Arg4": reasonMessage, "Arg5": lastID, "Arg6": summaryMessage, "Arg7": context.DeadlineExceeded}})
	if !actionable {
		return &botNoResourceError{err: err}
	}
	return err
}
