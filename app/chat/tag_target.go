package chat

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
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
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return target, fmt.Errorf("chat is required")
	}
	if !strings.ContainsAny(raw, "/.:") {
		target.Chat = strings.TrimPrefix(raw, "@")
	} else {
		if !strings.Contains(raw, "://") {
			raw = "https://" + strings.TrimPrefix(raw, "//")
		}
		u, err := url.Parse(raw)
		if err != nil {
			return target, fmt.Errorf("invalid chat URL: %w", err)
		}
		host := strings.ToLower(u.Hostname())
		if (u.Scheme != "https" && u.Scheme != "http") ||
			(host != "t.me" && host != "telegram.me" && host != "telegram.dog") || u.User != nil || u.Port() != "" {
			return target, fmt.Errorf("chat_url must be an http(s) Telegram link")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if parts[0] == "s" {
			parts = parts[1:]
		}
		if len(parts) == 0 || parts[0] == "" {
			return target, fmt.Errorf("chat_url is missing a chat")
		}
		if parts[0] == "c" {
			parts = parts[1:]
			if len(parts) == 0 {
				return target, fmt.Errorf("chat_url is missing a private chat ID")
			}
			if id, err := strconv.ParseInt(parts[0], 10, 64); err != nil || id <= 0 {
				return target, fmt.Errorf("private chat ID must be a positive integer")
			}
		}
		if len(parts) > 3 {
			return target, fmt.Errorf("chat_url must identify a chat or forum topic")
		}
		target.Chat = parts[0]
		for i, part := range parts[1:] {
			id, err := strconv.Atoi(part)
			if err != nil || id <= 0 {
				return target, fmt.Errorf("topic/message ID must be a positive integer")
			}
			if i == 0 {
				target.TopicID = id
			}
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return target, fmt.Errorf("invalid chat URL query: %w", err)
		}
		for key := range q {
			if key != "thread" {
				return target, fmt.Errorf("tag job chat_url does not support %q; use a chat or topic link", key)
			}
		}
		if q.Has("thread") {
			values := q["thread"]
			if len(values) != 1 {
				return target, fmt.Errorf("chat_url must contain only one thread ID")
			}
			id, err := strconv.Atoi(values[0])
			if err != nil || id <= 0 {
				return target, fmt.Errorf("thread ID must be a positive integer")
			}
			if len(parts) == 3 && target.TopicID != id {
				return target, fmt.Errorf("thread ID conflicts with the topic ID in chat_url")
			}
			target.TopicID = id
		}
	}
	if !telegramUsername.MatchString(target.Chat) {
		return target, fmt.Errorf("invalid chat name or ID %q", target.Chat)
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
