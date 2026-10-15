package autodl

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/iyear/tdl/app/chat"
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/util/tutil"
)

// resolveDialog resolves the peer that actually holds the messages.
//
// Comment mode is what the python script expressed by appending ?comment=N to
// the post link: the message ids are comments, so they live in the linked
// discussion group of the channel and not in the channel itself. The mode is
// therefore taken from the job ("comment": true) as well as from the link
// (?comment=N is also accepted), because the python config points chat_url at
// the post and keeps the range in start_comment/end_comment.
func (r *Runner) resolveDialog(ctx context.Context, job *Job, link Link) (peers.Peer, error) {
	key := fmt.Sprintf("%t:%s", commentDialog(job, link), link.Chat)
	if peer, ok := r.dialogs[key]; ok {
		return peer, nil
	}
	peer, err := r.resolveDialogUncached(ctx, job, link)
	if err == nil {
		if r.dialogs == nil {
			r.dialogs = make(map[string]peers.Peer)
		}
		r.dialogs[key] = peer
	}
	return peer, err
}

func (r *Runner) resolveDialogUncached(ctx context.Context, job *Job, link Link) (peers.Peer, error) {
	peer, err := tutil.GetInputPeer(ctx, r.manager, link.Chat)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrapf(err, "resolve chat %q", link.Chat), corei18n.Message{ID: "errors.context.resolve_chat_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", link.Chat), "Reason": err}})
	}

	if !commentDialog(job, link) {
		return peer, nil
	}

	ch, ok := peer.(peers.Channel)
	if !ok {
		return nil, diagnostic.Describe(errors.Errorf("chat %q has no comment section", link.Chat), corei18n.Message{ID: "errors.message.chat_value_has_no_comment_section", Args: map[string]any{"Arg1": fmt.Sprintf("%q", link.Chat)}})
	}

	raw, err := ch.FullRaw(ctx)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "get channel full"), corei18n.Message{ID: "errors.context.get_channel_full", Args: map[string]any{"Reason": err}})
	}

	linked, ok := raw.GetLinkedChatID()
	if !ok {
		return nil, diagnostic.Describe(errors.Errorf("chat %q has no linked discussion group", link.Chat), corei18n.Message{ID: "errors.message.chat_value_has_no_linked_discussion_group", Args: map[string]any{"Arg1": fmt.Sprintf("%q", link.Chat)}})
	}

	group, err := r.manager.ResolveChannelID(ctx, linked)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "resolve discussion group"), corei18n.Message{ID: "errors.context.resolve_discussion_group", Args: map[string]any{"Reason": err}})
	}

	logctx.From(ctx).Debug("Resolve comment dialog",
		zap.String("chat", link.Chat),
		zap.Int64("linked", linked),
		zap.Int64("id", group.ID()))

	return group, nil
}

// commentDialog reports whether the message ids of a job are comment ids in the
// linked discussion group of the channel.
//
// Both spellings are accepted: the job level "comment": true of the python
// config and the ?comment=N syntax of a telegram link. They mean the same
// thing, and getting this wrong silently resolves the ids against the wrong
// chat, where every single one of them looks deleted.
func commentDialog(job *Job, link Link) bool {
	return link.CommentMode() || job.CommentMode()
}

// jobTopic returns the verified forum topic a message job is limited to, or
// zero for the whole chat. Forum message IDs are shared by all topics, so a
// topic link such as https://t.me/c/1/24568 must narrow the scan instead of
// selecting a range or window of the complete chat. In a forum a bare
// /c/chat/ID path never falls back to the whole chat: when ID is not an
// accessible topic the job fails and the home URL selects all topics.
func (r *Runner) jobTopic(ctx context.Context, job *Job, link Link, peer peers.Peer) (int, string, error) {
	id := num(job.TopicID)
	if commentDialog(job, link) || job.ReplyPostID != nil {
		// The post ID of a comment or reply job identifies the post, not a topic.
		if link.TopicID > 0 {
			return 0, "", diagnostic.Describe(errors.New("chat_url topic links cannot be combined with comment mode or reply_post_id"), corei18n.Message{ID: "errors.batch.topic_link_selector"})
		}
	} else if id == 0 {
		id = link.TopicID
		if channel, ok := peer.(peers.Channel); ok && id == 0 && link.MessageID > 0 && channel.Raw().Forum {
			id = link.MessageID
		}
	}
	if id == 0 {
		return 0, "", nil
	}
	topic, err := chat.ResolveForumTopic(ctx, r.pool.Default(ctx), peer.InputPeer(), id)
	if err != nil {
		return 0, "", diagnostic.Describe(errors.Wrapf(err, "resolve topic_id %d", id), corei18n.Message{ID: "errors.context.resolve_topic_key_id_value", Args: map[string]any{"Arg1": id, "Reason": err}})
	}
	return id, topic.Title, nil
}

// inTopic reports whether m belongs to the forum topic rooted at topicID.
// Direct replies often omit ReplyToTopID, in which case ReplyToMsgID itself is
// the topic root.
func inTopic(m *tg.Message, topicID int) bool {
	if m.ID == topicID {
		return true
	}
	header, ok := m.ReplyTo.(*tg.MessageReplyHeader)
	if !ok {
		return false
	}
	id := header.ReplyToMsgID
	if top, ok := header.GetReplyToTopID(); ok {
		id = top
	}
	return id == topicID
}

// scanPeer resolves the dialog that incremental mode reads its window from.
//
// The python script exported job.chat when it was set and the chat of the link
// otherwise. An explicit job.chat is kept, because that is how the python
// config pointed the scan at a discussion group while chat_url kept pointing at
// the post.
func (r *Runner) scanPeer(ctx context.Context, job *Job, link Link) (peers.Peer, error) {
	expected, err := r.resolveDialog(ctx, job, link)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(job.Chat)
	if ref == "" {
		return expected, nil
	}

	// a job.chat may also be a full telegram link
	if l, err := ParseLink(ref); err == nil {
		ref = l.Chat
	}

	explicit, err := tutil.GetInputPeer(ctx, r.manager, ref)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrapf(err, "resolve chat override %q", job.Chat), corei18n.Message{ID: "errors.context.resolve_chat_override_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", job.Chat), "Reason": err}})
	}
	if peerKey(explicit) != peerKey(expected) {
		return nil, diagnostic.Describe(errors.Errorf("chat override %q resolves to %s, but chat_url/comment mode selects %s; scan and download sources must match", job.Chat, peerKey(explicit), peerKey(expected)), corei18n.Message{ID: "errors.batch.source_mismatch", Args: map[string]any{"Arg1": fmt.Sprintf("%q", job.Chat), "Arg2": peerKey(explicit), "Arg3": peerKey(expected)}})
	}
	return expected, nil
}

func peerKey(peer peers.Peer) string {
	if peer == nil {
		return ""
	}
	switch p := peer.InputPeer().(type) {
	case *tg.InputPeerChannel:
		return fmt.Sprintf("channel:%d", p.ChannelID)
	case *tg.InputPeerChat:
		return fmt.Sprintf("chat:%d", p.ChatID)
	case *tg.InputPeerUser:
		return fmt.Sprintf("user:%d", p.UserID)
	default:
		return fmt.Sprintf("%T:%d", peer.InputPeer(), peer.ID())
	}
}
