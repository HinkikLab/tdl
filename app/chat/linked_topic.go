package chat

import (
	"context"
	"fmt"
	"sort"

	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

func unsupportedLinkedService(m *tg.MessageService) error {
	return diagnostic.Describe(fmt.Errorf("message %d has unsupported service action %T", m.ID, m.Action), corei18n.Message{ID: "errors.message.message_value_has_unsupported_service_action_value", Args: map[string]any{"Arg1": m.ID, "Arg2": fmt.Sprintf("%T", m.Action)}})
}

// Telegram message IDs are scoped to their peer. A matching numeric ID from a
// different chat (or a different peer kind) must never become an archive input.
func validateLinkedPeer(peer tg.InputPeerClass, raw tg.MessageClass) error {
	var actual tg.PeerClass
	switch m := raw.(type) {
	case *tg.Message:
		actual = m.PeerID
	case *tg.MessageService:
		actual = m.PeerID
	case *tg.MessageEmpty:
		actual = m.PeerID
		if actual == nil {
			return nil // A deleted placeholder may omit its peer.
		}
	default:
		return diagnostic.Describe(fmt.Errorf("unexpected linked message type %T", raw), corei18n.Message{ID: "errors.message.unexpected_linked_message_type_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", raw)}})
	}
	match := false
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		a, ok := actual.(*tg.PeerChannel)
		match = ok && a.ChannelID == p.ChannelID
	case *tg.InputPeerChat:
		a, ok := actual.(*tg.PeerChat)
		match = ok && a.ChatID == p.ChatID
	case *tg.InputPeerUser:
		a, ok := actual.(*tg.PeerUser)
		match = ok && a.UserID == p.UserID
	}
	if !match {
		return func() error {
			messageArg1 := raw.GetID()
			messageArg2 := archiveSource(peer)
			return diagnostic.Describe(fmt.Errorf("linked message %d peer mismatch: requested %s, received %T", messageArg1, messageArg2, actual), corei18n.Message{ID: "errors.message.linked_message_value_peer_mismatch_requested_value_received_value", Args: map[string]any{"Arg1": messageArg1, "Arg2": messageArg2, "Arg3": fmt.Sprintf("%T", actual)}})
		}()
	}
	return nil
}

// Only resource links enter this branch. Primary-source selection continues to
// use messageAlbum, which requires an ordinary message and rejects service roots.
func (b *telegramLinkBackend) resourceMessages(ctx context.Context, peer tg.InputPeerClass, id int, single bool, topicID int) ([]resourceMessage, error) {
	raw, err := getLinkedRawMessage(ctx, b.api, peer, id)
	if err != nil {
		return nil, err
	}
	switch m := raw.(type) {
	case *tg.Message:
		if topicID > 0 {
			if err := verifyLinkedTopic(ctx, b.api, peer, topicID); err != nil {
				return nil, err
			}
			if !linkedTopicMember(m, topicID) {
				return nil, diagnostic.Describe(fmt.Errorf("message %d does not belong to linked forum topic %d", m.ID, topicID), corei18n.Message{ID: "errors.message.message_value_does_not_belong_to_linked_forum_topic_value", Args: map[string]any{"Arg1": m.ID, "Arg2": topicID}})
			}
		}
		return b.albumFromMessage(ctx, peer, m, single, topicID)
	case *tg.MessageService:
		if _, ok := m.Action.(*tg.MessageActionTopicCreate); !ok {
			return nil, unsupportedLinkedService(m)
		}
		if topicID > 0 && topicID != m.ID {
			return nil, diagnostic.Describe(fmt.Errorf("topic creation message %d conflicts with linked forum topic %d", m.ID, topicID), corei18n.Message{ID: "errors.message.topic_creation_message_value_conflicts_with_linked_forum_topic_value", Args: map[string]any{"Arg1": m.ID, "Arg2": topicID}})
		}
		return b.topicMessages(ctx, peer, m)
	default:
		return nil, diagnostic.Describe(fmt.Errorf("message %d is unavailable or deleted", id), corei18n.Message{ID: "errors.message.message_value_is_unavailable_or_deleted", Args: map[string]any{"Arg1": id}})
	}
}

func verifyLinkedTopic(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, id int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	channel, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return diagnostic.Describe(fmt.Errorf("linked topic %d requires a forum channel", id), corei18n.Message{ID: "errors.message.linked_topic_value_requires_a_forum_channel", Args: map[string]any{"Arg1": id}})
	}
	res, err := api.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: peer, Topics: []int{id}})
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("verify linked forum topic %d: %w", id, err), corei18n.Message{ID: "errors.message.verify_linked_forum_topic_value_value", Args: map[string]any{"Arg1": id, "Arg2": err}})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	forum, topic := false, false
	for _, raw := range res.Chats {
		if c, ok := raw.(*tg.Channel); ok && c.ID == channel.ChannelID && c.Forum {
			forum = true
		}
	}
	for _, raw := range res.Topics {
		if t, ok := raw.(*tg.ForumTopic); ok && t.ID == id {
			p, ok := t.Peer.(*tg.PeerChannel)
			topic = ok && p.ChannelID == channel.ChannelID
		}
	}
	if !forum || !topic {
		return diagnostic.Describe(fmt.Errorf("linked service message %d is not a confirmed accessible forum topic", id), corei18n.Message{ID: "errors.message.linked_service_message_value_is_not_a_confirmed_accessible_forum_topic", Args: map[string]any{"Arg1": id}})
	}
	return nil
}

func linkedTopicMember(raw tg.MessageClass, topicID int) bool {
	if raw.GetID() == topicID {
		return true
	}
	var reply tg.MessageReplyHeaderClass
	switch m := raw.(type) {
	case *tg.Message:
		reply = m.ReplyTo
	case *tg.MessageService:
		reply = m.ReplyTo
	case *tg.MessageEmpty:
		return true // A placeholder carries no remaining thread information.
	}
	header, ok := reply.(*tg.MessageReplyHeader)
	if !ok || !header.ForumTopic {
		return false
	}
	if header.ReplyToTopID != 0 {
		return header.ReplyToTopID == topicID
	}
	return header.ReplyToMsgID == topicID
}

func (b *telegramLinkBackend) topicMessages(ctx context.Context, peer tg.InputPeerClass, root *tg.MessageService) ([]resourceMessage, error) {
	if err := verifyLinkedTopic(ctx, b.api, peer, root.ID); err != nil {
		return nil, err
	}
	limit := b.opts.MaxTopicMessages
	if limit == 0 {
		limit = 1000
	}
	if limit < 1 || limit > 100000 {
		return nil, diagnostic.Describe(fmt.Errorf("max_topic_messages must be between 1 and 100000"), corei18n.Message{ID: "errors.message.max_key_topic_key_messages_must_be_between_1_and_100000"})
	}
	// Count the root, service messages and deleted placeholders too. Keeping the
	// whole bounded thread lets albums span pages without extra grouped lookups.
	seen := map[int]bool{root.ID: true}
	var result []resourceMessage
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := b.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{Peer: peer, MsgID: root.ID, OffsetID: offset, Limit: 100})
		if err != nil {
			return nil, diagnostic.Describe(fmt.Errorf("read linked forum topic %d: %w", root.ID, err), corei18n.Message{ID: "errors.message.read_linked_forum_topic_value_value", Args: map[string]any{"Arg1": root.ID, "Arg2": err}})
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, ok := res.AsModified()
		if !ok {
			return nil, diagnostic.Describe(fmt.Errorf("unexpected linked topic result %T", res), corei18n.Message{ID: "errors.message.unexpected_linked_topic_result_value", Args: map[string]any{"Arg1": fmt.Sprintf("%T", res)}})
		}
		msgs := page.GetMessages()
		if len(msgs) == 0 {
			break
		}
		next := 0
		for _, raw := range msgs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			id := raw.GetID()
			if id <= 0 {
				return nil, diagnostic.Describe(fmt.Errorf("linked topic %d returned invalid message ID %d", root.ID, id), corei18n.Message{ID: "errors.message.linked_topic_value_returned_invalid_message_id_value", Args: map[string]any{"Arg1": root.ID, "Arg2": id}})
			}
			if err := validateLinkedPeer(peer, raw); err != nil {
				return nil, err
			}
			if !linkedTopicMember(raw, root.ID) {
				return nil, diagnostic.Describe(fmt.Errorf("message %d does not belong to linked forum topic %d", id, root.ID), corei18n.Message{ID: "errors.message.message_value_does_not_belong_to_linked_forum_topic_value", Args: map[string]any{"Arg1": id, "Arg2": root.ID}})
			}
			if id != root.ID && (next == 0 || id < next) {
				next = id
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			if len(seen) > limit {
				return nil, diagnostic.Describe(fmt.Errorf("linked forum topic %d exceeds max_topic_messages (%d)", root.ID, limit), corei18n.Message{ID: "errors.message.linked_forum_topic_value_exceeds_max_key_topic_key_messages_value", Args: map[string]any{"Arg1": root.ID, "Arg2": limit}})
			}
			if m, ok := raw.(*tg.Message); ok {
				result = append(result, resourceMessage{Peer: peer, Message: m})
			}
		}
		if next == 0 {
			break // Only the root was returned; there are no remaining replies.
		}
		if offset != 0 && next >= offset {
			return nil, diagnostic.Describe(fmt.Errorf("linked forum topic %d pagination did not advance", root.ID), corei18n.Message{ID: "errors.message.linked_forum_topic_value_pagination_did_not_advance", Args: map[string]any{"Arg1": root.ID}})
		}
		offset = next
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Message.ID < result[j].Message.ID })
	return result, nil
}
