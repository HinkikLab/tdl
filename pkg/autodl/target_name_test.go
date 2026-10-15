package autodl

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestMessageJobSelectsForumTopicFromChatURL(t *testing.T) {
	for _, test := range []struct {
		name   string
		url    string
		forum  bool
		topic  *int
		known  bool
		want   string
		lookup []int
		err    string
	}{
		{name: "topic path", url: "https://t.me/c/2255983776/41872/", forum: true, known: true, want: "Example forum (2255983776) / Guides (41872)", lookup: []int{41872}},
		{name: "topic message path", url: "https://t.me/c/2255983776/41872/42000", forum: true, known: true, want: "Example forum (2255983776) / Guides (41872)", lookup: []int{41872}},
		{name: "thread query", url: "https://t.me/c/2255983776/42000?thread=41872", forum: true, known: true, want: "Example forum (2255983776) / Guides (41872)", lookup: []int{41872}},
		{name: "explicit topic_id", url: "https://t.me/c/2255983776/", forum: true, topic: ptr(41872), known: true, want: "Example forum (2255983776) / Guides (41872)", lookup: []int{41872}},
		{name: "home URL", url: "https://t.me/c/2255983776/", forum: true, want: "Example forum (2255983776) / all topics"},
		{name: "plain channel post", url: "https://t.me/c/2255983776/41872", want: "Example forum (2255983776)"},
		{name: "forum non-topic message", url: "https://t.me/c/2255983776/41872", forum: true, lookup: []int{41872}, err: "does not identify an accessible forum topic (41872)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var lookups []int
			api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				req, ok := in.(*tg.MessagesGetForumTopicsByIDRequest)
				if !ok {
					return fmt.Errorf("unexpected RPC %T", in)
				}
				lookups = append(lookups, req.Topics...)
				res := &tg.MessagesForumTopics{}
				if test.known {
					res.Topics = []tg.ForumTopicClass{&tg.ForumTopic{ID: 41872, Title: "Guides", FromID: &tg.PeerUser{UserID: 1}, Peer: &tg.PeerChannel{ChannelID: 2255983776}}}
				}
				return encodeResult(res, out)
			}))
			peer := (&peers.Manager{}).Channel(&tg.Channel{ID: 2255983776, Title: "Example forum", Forum: test.forum})
			start, end := 1, 2
			job := &Job{ChatURL: test.url, StartComment: &start, EndComment: &end, TopicID: test.topic, dir: t.TempDir()}
			runner := &Runner{cfg: &Config{}, pool: batchPool{api}, opts: Options{CheckOnly: true}, dialogs: map[string]peers.Peer{"false:2255983776": peer}}
			var targets []string
			err := runner.runJob(context.Background(), job, 1, 1, func(target string) { targets = append(targets, target) })
			require.Equal(t, test.lookup, lookups)
			if test.err != "" {
				require.ErrorContains(t, err, test.err)
				require.Empty(t, targets)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{test.want}, targets)
			require.Equal(t, test.topic, job.TopicID, "the shared job snapshot must stay unchanged")
		})
	}
}

func TestRangeIteratorKeepsOnlyTopicMessages(t *testing.T) {
	state := NewState()
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.ChannelsGetMessagesRequest)
		require.True(t, ok)
		replyTo := map[int]*tg.MessageReplyHeader{
			11: {ReplyToMsgID: 10},
			12: {ReplyToMsgID: 11, ReplyToTopID: 10},
			13: {ReplyToMsgID: 20},
			15: {ReplyToMsgID: 11, ReplyToTopID: 20},
		}
		var messages []tg.MessageClass
		for _, value := range req.ID {
			id := value.(*tg.InputMessageID).ID
			m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 50}}
			if header := replyTo[id]; header != nil {
				if header.ReplyToTopID != 0 {
					header.SetReplyToTopID(header.ReplyToTopID)
				}
				m.SetReplyTo(header)
			}
			messages = append(messages, m)
		}
		return encodeResult(&tg.MessagesChannelMessages{Messages: messages}, out)
	}))
	manager, dialog := newIteratorTestChannel(t, api)
	it, err := newIter(batchPool{api}, manager, dialog, t.TempDir(), nil, &iterOptions{
		state: state, template: "{{ .DialogID }}_{{ .MessageID }}_{{ .FileName }}",
		rangeStart: 10, rangeEnd: 16, topicID: 10, isFinished: state.IsTerminalWithoutMedia,
		onSkip: func(ids []int) { state.Skip(ids...) },
	})
	require.NoError(t, err)
	require.False(t, it.Next(context.Background()))
	require.NoError(t, it.Err())
	require.Equal(t, map[int]struct{}{10: {}, 11: {}, 12: {}}, state.skipped, "topic messages are scanned")
	require.Equal(t, map[int]struct{}{13: {}, 14: {}, 15: {}}, state.filtered, "other topics are excluded")
	require.Zero(t, countMissingRange(state, 10, 16))
}
