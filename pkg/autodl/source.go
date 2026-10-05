package autodl

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

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
		return nil, func() error {
			messageArg2 := peerKey(explicit)
			messageArg3 := peerKey(expected)
			return diagnostic.Describe(errors.Errorf("chat override %q resolves to %s, but chat_url/comment mode selects %s; scan and download sources must match", job.Chat, messageArg2, messageArg3), corei18n.Message{ID: "errors.batch.source_mismatch", Args: map[string]any{"Arg1": fmt.Sprintf("%q", job.Chat), "Arg2": messageArg2, "Arg3": messageArg3}})
		}()
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
