// Package tgref parses Telegram references without deciding how a command
// selects messages, topics, comments or bot resources.
package tgref

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
		return ref, fmt.Errorf("empty Telegram reference")
	}
	if opts.AllowBare && !strings.ContainsAny(s, "/.:") {
		ref.Chat = strings.TrimPrefix(s, "@")
		if !username.MatchString(ref.Chat) {
			return ref, fmt.Errorf("invalid chat name or ID %q", ref.Chat)
		}
		return ref, nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + strings.TrimPrefix(s, "//")
	}
	u, err := url.Parse(s)
	if err != nil {
		return ref, fmt.Errorf("invalid Telegram URL: %w", err)
	}
	if u.User != nil || u.Port() != "" || u.Fragment != "" || strings.HasSuffix(u.Host, ":") {
		return ref, fmt.Errorf("invalid Telegram host or fragment")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ref, fmt.Errorf("invalid Telegram URL query: %w", err)
	}
	ref.Query = q
	for _, key := range []string{"domain", "channel", "post", "thread", "comment", "start", "single"} {
		if len(q[key]) > 1 {
			return ref, fmt.Errorf("Telegram URL must contain only one %s value", key)
		}
	}
	private := false
	switch strings.ToLower(u.Scheme) {
	case "tg":
		if !opts.AllowTG || (u.Path != "" && u.Path != "/") {
			return ref, fmt.Errorf("unsupported Telegram action")
		}
		switch strings.ToLower(u.Host) {
		case "resolve":
			ref.Chat = q.Get("domain")
		case "privatepost":
			ref.Chat, private = q.Get("channel"), true
		default:
			return ref, fmt.Errorf("unsupported Telegram action")
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
			return ref, fmt.Errorf("unsupported link host %q (expected t.me)", u.Hostname())
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) > 0 && strings.EqualFold(parts[0], "s") {
			parts = parts[1:]
		}
		if len(parts) > 0 && strings.EqualFold(parts[0], "c") {
			parts, private = parts[1:], true
		}
		if len(parts) == 0 || parts[0] == "" || len(parts) > 3 {
			return ref, fmt.Errorf("invalid Telegram chat/message link")
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
		return ref, fmt.Errorf("Telegram link must use http(s)")
	}
	if !username.MatchString(ref.Chat) {
		return ref, fmt.Errorf("invalid chat name or ID %q", ref.Chat)
	}
	if private {
		if id, e := strconv.ParseInt(ref.Chat, 10, 64); e != nil || id <= 0 {
			return ref, fmt.Errorf("private chat ID must be a positive integer")
		}
	}
	if q.Has("thread") {
		id, e := positiveID(q.Get("thread"), "thread ID")
		if e != nil {
			return ref, e
		}
		if ref.TopicID != 0 && ref.TopicID != id {
			return ref, fmt.Errorf("thread ID conflicts with the topic ID in chat_url")
		}
		ref.TopicID = id
	}
	if q.Has("comment") {
		ref.CommentID, err = positiveID(q.Get("comment"), "comment ID")
		if err != nil {
			return ref, err
		}
		if ref.MessageID == 0 {
			return ref, fmt.Errorf("comment link requires a post ID")
		}
	}
	ref.Bot, ref.Start, ref.Single = q.Has("start"), q.Get("start"), q.Has("single")
	if ref.Bot && (ref.MessageID != 0 || ref.CommentID != 0 || ref.TopicID != 0 || private) {
		return ref, fmt.Errorf("invalid bot link")
	}
	return ref, nil
}

func positiveID(s, name string) (int, error) {
	id, err := strconv.Atoi(s)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return id, nil
}
