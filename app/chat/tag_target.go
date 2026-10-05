package chat

import (
	"context"
	"fmt"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"

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
		return target, fmt.Errorf("topic_id must be positive")
	}
	ref, err := tgref.Parse(raw, tgref.Options{AllowBare: true})
	if err != nil {
		return target, err
	}
	for key := range ref.Query {
		if key != "thread" {
			return target, fmt.Errorf("tag job chat_url does not support %q; use a chat or topic link", key)
		}
	}
	target.Chat, target.TopicID = ref.Chat, ref.TopicID
	if target.TopicID == 0 {
		target.TopicID = ref.MessageID
	}
	if topicID > 0 {
		if target.TopicID != 0 && target.TopicID != topicID {
			return target, fmt.Errorf("topic_id (%d) conflicts with chat_url topic (%d)", topicID, target.TopicID)
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
		return nil, fmt.Errorf("verify forum topic %d: %w", topicID, err)
	}
	for _, value := range topics.Topics {
		if topic, ok := value.(*tg.ForumTopic); ok && topic.ID == topicID {
			return topic, nil
		}
	}
	return nil, fmt.Errorf("chat_url/topic_id does not identify an accessible forum topic (%d); use the chat home URL to scan the whole chat", topicID)
}
