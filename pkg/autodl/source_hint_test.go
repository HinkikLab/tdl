package autodl

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Source URL topic hints identify the context of a concrete main message.
// Resource topic expansion belongs to the linked resource backend alone.
func TestFollowLinksSourceTopicHintPreservesMainMessageSelection(t *testing.T) {
	for _, test := range []struct{ url, chat string }{
		{"https://t.me/group/42/100", "group"},
		{"https://t.me/group/100?thread=42", "group"},
		{"https://t.me/c/123456/42/100", "123456"},
		{"https://t.me/c/123456/100?thread=42", "123456"},
	} {
		t.Run(test.url, func(t *testing.T) {
			link, err := ParseLink(test.url)
			require.NoError(t, err)
			require.Equal(t, Link{Chat: test.chat, MessageID: 100}, link)
			cfg := &Config{Jobs: []Job{{ChatURL: test.url, FollowLinks: true}}}
			require.NoError(t, cfg.Normalize())
			require.Nil(t, cfg.Jobs[0].TopicID)
			require.Nil(t, cfg.Jobs[0].ReplyPostID)
		})
	}
}

func TestFollowLinksExplicitPrimaryTopicAndReplySelectorsRemainUnsupported(t *testing.T) {
	for _, selector := range []string{"topic_id", "reply_post_id"} {
		t.Run(selector, func(t *testing.T) {
			job := Job{ChatURL: "https://t.me/group/42/100", FollowLinks: true}
			if selector == "topic_id" {
				job.TopicID = ptr(42)
			} else {
				job.ReplyPostID = ptr(100)
			}
			cfg := &Config{Jobs: []Job{job}}
			require.ErrorContains(t, cfg.Normalize(), "comment/topic selectors are not supported")
		})
	}
}

func TestNormalIncrementalURLTopicHintDoesNotImplicitlySetTopicSelector(t *testing.T) {
	for _, url := range []string{"https://t.me/group/42/100", "https://t.me/group/100?thread=42"} {
		cfg := &Config{Jobs: []Job{{ChatURL: url, Incremental: ptr(true)}}}
		require.NoError(t, cfg.Normalize())
		require.Nil(t, cfg.Jobs[0].TopicID)
		cfg.Jobs[0].TopicID = ptr(42)
		require.NoError(t, cfg.Normalize())
		require.Equal(t, 42, *cfg.Jobs[0].TopicID)
	}
}
