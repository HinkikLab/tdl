// Package tgref parses Telegram references without deciding how a command
// selects messages, topics, comments or bot resources.
package tgref

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

type Options struct {
	AllowBare bool
	AllowTG   bool
}

type Ref struct {
	Chat      string
	MessageID int
	TopicID   int
	CommentID int
	Start     string
	Bot       bool
	Single    bool
	Query     url.Values
}

var username = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Parse validates the common syntax. Callers enforce their capability matrix:
// a topic-looking path, for example, still needs server-side forum validation.
func Parse(raw string, opts Options) (Ref, error) {
	var ref Ref
	s := strings.TrimSpace(raw)
	if s == "" {
		return ref, diagnostic.Describe(fmt.Errorf("empty Telegram reference"), corei18n.Message{ID: "errors.message.empty_telegram_reference"})
	}
	if opts.AllowBare && !strings.ContainsAny(s, "/.:") {
		ref.Chat = strings.TrimPrefix(s, "@")
		if !username.MatchString(ref.Chat) {
			return ref, diagnostic.Describe(fmt.Errorf("invalid chat name or ID %q", ref.Chat), corei18n.Message{ID: "errors.message.invalid_chat_name_or_id_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", ref.Chat)}})
		}
		return ref, nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + strings.TrimPrefix(s, "//")
	}
	u, err := url.Parse(s)
	if err != nil {
		return ref, diagnostic.Describe(fmt.Errorf("invalid Telegram URL: %w", err), corei18n.Message{ID: "errors.message.invalid_telegram_url_value", Args: map[string]any{"Arg1": err}})
	}
	if u.User != nil || u.Port() != "" || u.Fragment != "" || strings.HasSuffix(u.Host, ":") {
		return ref, diagnostic.Describe(fmt.Errorf("invalid Telegram host or fragment"), corei18n.Message{ID: "errors.message.invalid_telegram_host_or_fragment"})
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ref, diagnostic.Describe(fmt.Errorf("invalid Telegram URL query: %w", err), corei18n.Message{ID: "errors.message.invalid_telegram_url_query_value", Args: map[string]any{"Arg1": err}})
	}
	ref.Query = q
	for _, key := range []string{"domain", "channel", "post", "thread", "comment", "start", "single"} {
		if len(q[key]) > 1 {
			//nolint:staticcheck // Telegram is a proper name in the preserved diagnostic.
			return ref, diagnostic.Describe(fmt.Errorf("Telegram URL must contain only one %s value", key), corei18n.Message{ID: "errors.message.telegram_url_must_contain_only_one_value_value", Args: map[string]any{"Arg1": key}})
		}
	}
	private := false
	switch strings.ToLower(u.Scheme) {
	case "tg":
		if !opts.AllowTG || (u.Path != "" && u.Path != "/") {
			return ref, diagnostic.Describe(fmt.Errorf("unsupported Telegram action"), corei18n.Message{ID: "errors.message.unsupported_telegram_action"})
		}
		switch strings.ToLower(u.Host) {
		case "resolve":
			ref.Chat = q.Get("domain")
		case "privatepost":
			ref.Chat, private = q.Get("channel"), true
		default:
			return ref, diagnostic.Describe(fmt.Errorf("unsupported Telegram action"), corei18n.Message{ID: "errors.message.unsupported_telegram_action"})
		}
		if q.Has("post") {
			ref.MessageID, err = positiveID(q.Get("post"), "post ID")
			if err != nil {
				return ref, err
			}
		}
	case "http", "https":
		switch strings.ToLower(u.Hostname()) {
		case "t.me", "telegram.me", "telegram.dog":
		default:
			return ref, diagnostic.Describe(fmt.Errorf("unsupported link host %q (expected t.me)", u.Hostname()), corei18n.Message{ID: "errors.message.unsupported_link_host_value_expected_t_me", Args: map[string]any{"Arg1": fmt.Sprintf("%q", u.Hostname())}})
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) > 0 && strings.EqualFold(parts[0], "s") {
			parts = parts[1:]
		}
		if len(parts) > 0 && strings.EqualFold(parts[0], "c") {
			parts, private = parts[1:], true
		}
		if len(parts) == 0 || parts[0] == "" || len(parts) > 3 {
			return ref, diagnostic.Describe(fmt.Errorf("invalid Telegram chat/message link"), corei18n.Message{ID: "errors.message.invalid_telegram_chat_message_link"})
		}
		ref.Chat = parts[0]
		for i, part := range parts[1:] {
			id, e := positiveID(part, "topic/message ID")
			if e != nil {
				return ref, e
			}
			ref.MessageID = id
			if len(parts) == 3 && i == 0 {
				ref.TopicID = id
			}
		}
	default:
		//nolint:staticcheck // Telegram is a proper name in the preserved diagnostic.
		return ref, diagnostic.Describe(fmt.Errorf("Telegram link must use http(s)"), corei18n.Message{ID: "errors.message.telegram_link_must_use_http_s"})
	}
	if !username.MatchString(ref.Chat) {
		return ref, diagnostic.Describe(fmt.Errorf("invalid chat name or ID %q", ref.Chat), corei18n.Message{ID: "errors.message.invalid_chat_name_or_id_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", ref.Chat)}})
	}
	if private {
		if id, e := strconv.ParseInt(ref.Chat, 10, 64); e != nil || id <= 0 {
			return ref, diagnostic.Describe(fmt.Errorf("private chat ID must be a positive integer"), corei18n.Message{ID: "errors.message.private_chat_id_must_be_a_positive_integer"})
		}
	}
	if q.Has("thread") {
		id, e := positiveID(q.Get("thread"), "thread ID")
		if e != nil {
			return ref, e
		}
		if ref.TopicID != 0 && ref.TopicID != id {
			return ref, diagnostic.Describe(fmt.Errorf("thread ID conflicts with the topic ID in chat_url"), corei18n.Message{ID: "errors.message.thread_id_conflicts_with_the_topic_id_in_chat_key_url"})
		}
		ref.TopicID = id
	}
	if q.Has("comment") {
		ref.CommentID, err = positiveID(q.Get("comment"), "comment ID")
		if err != nil {
			return ref, err
		}
		if ref.MessageID == 0 {
			return ref, diagnostic.Describe(fmt.Errorf("comment link requires a post ID"), corei18n.Message{ID: "errors.message.comment_link_requires_a_post_id"})
		}
	}
	ref.Bot, ref.Start, ref.Single = q.Has("start"), q.Get("start"), q.Has("single")
	if ref.Bot && (ref.MessageID != 0 || ref.CommentID != 0 || ref.TopicID != 0 || private) {
		return ref, diagnostic.Describe(fmt.Errorf("invalid bot link"), corei18n.Message{ID: "errors.message.invalid_bot_link"})
	}
	return ref, nil
}

func positiveID(s, name string) (int, error) {
	id, err := strconv.Atoi(s)
	if err != nil || id <= 0 {
		return 0, diagnostic.Describe(fmt.Errorf("%s must be a positive integer", name), corei18n.Message{ID: "errors.message.value_must_be_a_positive_integer", Args: map[string]any{"Arg1": name}})
	}
	return id, nil
}
