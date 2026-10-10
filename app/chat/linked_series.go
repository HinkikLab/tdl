package chat

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/console"
	uimessages "github.com/iyear/tdl/pkg/messages"
)

const (
	seriesPage    = 50
	maxSeriesScan = 300
)

// seriesScope keeps a series inside the linked message's forum topic, or out of
// every forum topic when the message is not in one.
func seriesScope(anchor *tg.Message, topicID int) func(*tg.Message) bool {
	if topicID == 0 {
		if h, ok := anchor.ReplyTo.(*tg.MessageReplyHeader); ok && h.ForumTopic {
			topicID = h.ReplyToTopID
			if topicID == 0 {
				topicID = h.ReplyToMsgID
			}
		}
	}
	if topicID > 0 {
		return func(m *tg.Message) bool { return linkedTopicMember(m, topicID) }
	}
	return func(m *tg.Message) bool {
		h, ok := m.ReplyTo.(*tg.MessageReplyHeader)
		return !ok || !h.ForumTopic
	}
}

// threadMember reports whether m is a comment in the discussion thread of root.
func threadMember(m *tg.Message, root int) bool {
	h, ok := m.ReplyTo.(*tg.MessageReplyHeader)
	if !ok {
		return false
	}
	if h.ReplyToTopID != 0 {
		return h.ReplyToTopID == root
	}
	return h.ReplyToMsgID == root
}

func seriesSender(m *tg.Message) string {
	if m.FromID != nil {
		return fmt.Sprintf("%T:%d", m.FromID, tutil.GetPeerID(m.FromID))
	}
	return "post:" + m.PostAuthor
}

// seriesCaption keeps only letters, so "Title (1/3)" and "Title (2/3)" match.
func seriesCaption(text string) string {
	text = telegramURL.ReplaceAllString(text, " ")
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

// series extends a linked message (or album) with the media groups that its
// sender posted right after it, because Telegram limits an album to 10 items
// and larger resources are posted as several consecutive albums. Messages from
// other senders or other topics may interleave. The series ends at the first
// of these from the same sender: a message without a file, a message with
// resource links, a caption naming a different post, or a gap longer than
// series_gap_seconds. Only channels and supergroups have sequential message
// IDs, which the lookup relies on.
func (b *telegramLinkBackend) series(ctx context.Context, peer tg.InputPeerClass, set []resourceMessage, scope func(*tg.Message) bool) ([]resourceMessage, error) {
	channel, ok := peer.(*tg.InputPeerChannel)
	if !ok || len(set) == 0 || !defaultOn(b.opts.FollowSeries) {
		return set, nil
	}
	gap := b.opts.SeriesGap
	if gap <= 0 {
		gap = 120 // options that bypassed Normalize
	}
	anchor := set[0].Message
	sender := seriesSender(anchor)
	caption := ""
	last, lastDate := 0, 0
	for _, m := range set {
		last, lastDate = max(last, m.Message.ID), max(lastDate, m.Message.Date)
		if c := seriesCaption(m.Message.Message); caption == "" && c != "" {
			caption = c
		}
	}
	result := set
	added := 0
	// accept reports whether the series continues after unit.
	accept := func(unit []*tg.Message) bool {
		unitCaption := ""
		for _, m := range unit {
			if md, ok := tmedia.GetMedia(m); !ok || md.Size <= 0 || len(messageResourceLinks(m, nil)) > 0 {
				return false
			}
			if c := seriesCaption(m.Message); unitCaption == "" && c != "" {
				unitCaption = c
			}
		}
		if unitCaption != "" {
			if caption != "" && unitCaption != caption {
				return false
			}
			caption = unitCaption
		}
		for _, m := range unit {
			result = append(result, resourceMessage{Peer: peer, Message: m})
			lastDate = max(lastDate, m.Date)
		}
		added += len(unit)
		return true
	}
	var pending []*tg.Message
	flush := func() bool {
		unit := pending
		pending = nil
		return len(unit) == 0 || accept(unit)
	}
	done := func() []resourceMessage {
		if added > 0 {
			fmt.Println(console.Translate(ctx, uimessages.LinkedSeries(anchor.ID, added)))
		}
		return result
	}
	for next, scanned := last+1, 0; scanned < maxSeriesScan; next += seriesPage {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids := make([]tg.InputMessageClass, 0, seriesPage)
		for id := next; id < next+seriesPage; id++ {
			ids = append(ids, &tg.InputMessageID{ID: id})
		}
		res, err := b.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, ID: ids})
		if err != nil {
			return nil, diagnostic.Describe(fmt.Errorf("read messages after linked message %d: %w", anchor.ID, err), corei18n.Message{ID: "errors.linked.series_read", Args: map[string]any{"Arg1": anchor.ID, "Arg2": err}})
		}
		modified, ok := res.AsModified()
		if !ok {
			return nil, diagnostic.Describe(fmt.Errorf("unexpected message result %T", res), corei18n.Message{ID: "errors.message.unexpected_message_result_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", res)}})
		}
		byID := map[int]tg.MessageClass{}
		for _, raw := range modified.GetMessages() {
			if id := raw.GetID(); id >= next && id < next+seriesPage {
				byID[id] = raw
			}
		}
		empty := true
		for id := next; id < next+seriesPage && scanned < maxSeriesScan; id++ {
			scanned++
			raw := byID[id]
			if raw == nil {
				continue
			}
			if _, ok := raw.(*tg.MessageEmpty); ok {
				continue
			}
			empty = false
			if err := validateLinkedPeer(peer, raw); err != nil {
				return nil, err
			}
			m, ok := raw.(*tg.Message)
			if !ok {
				continue // service messages may interleave
			}
			// IDs follow send order, so nothing after a late message is in time.
			if m.Date > lastDate+gap {
				flush()
				return done(), nil
			}
			if !scope(m) || seriesSender(m) != sender {
				if !flush() {
					return done(), nil
				}
				continue
			}
			if len(pending) > 0 && m.GroupedID != 0 && m.GroupedID == pending[0].GroupedID {
				pending = append(pending, m)
				continue
			}
			if !flush() {
				return done(), nil
			}
			pending = []*tg.Message{m}
		}
		if empty {
			flush() // past the newest message
			return done(), nil
		}
	}
	// An album cut by the scan bound may be incomplete; leave it out.
	if len(pending) > 0 && pending[0].GroupedID == 0 {
		flush()
	}
	return done(), nil
}
