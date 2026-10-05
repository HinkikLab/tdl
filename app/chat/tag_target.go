package chat

import (
	"context"
	"fmt"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/util/tgref"
)

// TagTarget identifies a whole chat or one forum topic within it.
type TagTarget struct {
	Chat    string
	TopicID int
}

// ParseTagTarget accepts chat names, public/private home URLs and forum links.
// A topic root has the same URL shape as an ordinary message; tagHistory
// confirms it with Telegram before scanning, rather than widening to all history.
func ParseTagTarget(raw string, topicID int) (TagTarget, error) {
	var target TagTarget
	if topicID < 0 {
		return target, diagnostic.Describe(fmt.Errorf("topic_id must be positive"), corei18n.Message{ID: "errors.message.topic_key_id_must_be_positive"})
	}
	ref, err := tgref.Parse(raw, tgref.Options{AllowBare: true})
	if err != nil {
		return target, err
	}
	for key := range ref.Query {
		if key != "thread" {
			return target, diagnostic.Describe(fmt.Errorf("tag job chat_url does not support %q; use a chat or topic link", key), corei18n.Message{ID: "errors.message.tag_job_chat_key_url_does_not_support_value_use_a_chat_or_topic_link", Args: map[string]any{"Arg1": fmt.Sprintf("%q", key)}})
		}
	}
	target.Chat, target.TopicID = ref.Chat, ref.TopicID
	if target.TopicID == 0 {
		target.TopicID = ref.MessageID
	}
	if topicID > 0 {
		if target.TopicID != 0 && target.TopicID != topicID {
			return target, diagnostic.Describe(fmt.Errorf("topic_id (%d) conflicts with chat_url topic (%d)", topicID, target.TopicID), corei18n.Message{ID: "errors.message.topic_key_id_value_conflicts_with_chat_key_url_topic_value", Args: map[string]any{"Arg1": topicID, "Arg2": target.TopicID}})
		}
		target.TopicID = topicID
	}
	return target, nil
}

func tagHistory(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, topicID int) (messages.Query, *tg.ForumTopic, error) {
	q := query.NewQuery(api).Messages()
	if topicID == 0 {
		return q.GetHistory(peer), nil, nil
	}
	topic, err := ResolveForumTopic(ctx, api, peer, topicID)
	if err != nil {
		return nil, nil, err
	}
	return q.GetReplies(peer).MsgID(topicID), topic, nil
}

// ResolveForumTopic confirms the selector and returns the title from Telegram.
func ResolveForumTopic(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, topicID int) (*tg.ForumTopic, error) {
	topics, err := api.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{
		Peer: peer, Topics: []int{topicID},
	})
	if err != nil {
		return nil, diagnostic.Describe(fmt.Errorf("verify forum topic %d: %w", topicID, err), corei18n.Message{ID: "errors.message.verify_forum_topic_value_value", Args: map[string]any{"Arg1": topicID, "Arg2": err}})
	}
	for _, value := range topics.Topics {
		if topic, ok := value.(*tg.ForumTopic); ok && topic.ID == topicID {
			return topic, nil
		}
	}
	return nil, diagnostic.Describe(fmt.Errorf("chat_url/topic_id does not identify an accessible forum topic (%d); use the chat home URL to scan the whole chat", topicID), corei18n.Message{ID: "errors.chat.topic_inaccessible", Args: map[string]any{"Arg1": topicID}})
}
