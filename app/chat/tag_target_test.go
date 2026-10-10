package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestParseTagTarget(t *testing.T) {
	for _, test := range []struct {
		raw   string
		topic int
		want  TagTarget
	}{
		{"https://t.me/AVMYS/", 0, TagTarget{Chat: "AVMYS"}},
		{"@AVMYS", 0, TagTarget{Chat: "AVMYS"}},
		{"2255983776", 0, TagTarget{Chat: "2255983776"}},
		{"https://t.me/c/2255983776/", 0, TagTarget{Chat: "2255983776"}},
		{"https://t.me/c/2255983776/41872/", 0, TagTarget{Chat: "2255983776", TopicID: 41872}},
		{"t.me/c/2255983776/41872/42000", 0, TagTarget{Chat: "2255983776", TopicID: 41872}},
		{"https://telegram.me/group/41872/42000", 0, TagTarget{Chat: "group", TopicID: 41872}},
		{"https://t.me/group/42000?thread=41872", 0, TagTarget{Chat: "group", TopicID: 41872}},
		{"https://t.me/c/2255983776/42000?thread=41872", 0, TagTarget{Chat: "2255983776", TopicID: 41872}},
		{"https://t.me/s/group", 41872, TagTarget{Chat: "group", TopicID: 41872}},
		{"https://t.me/c/2255983776/41872", 41872, TagTarget{Chat: "2255983776", TopicID: 41872}},
		{"https://t.me/c/2255983776/1/", 0, TagTarget{Chat: "2255983776", TopicID: 1}},
	} {
		t.Run(fmt.Sprintf("%s:%d", test.raw, test.topic), func(t *testing.T) {
			got, err := ParseTagTarget(test.raw, test.topic)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestParseTagTargetRejectsInvalidOrConflictingSelectors(t *testing.T) {
	for _, raw := range []string{
		"", "@", "https://example.com/group", "file://t.me/group",
		"https://t.me:443/group", "https://user@t.me/group", "https://t.me/", "https://t.me/s/",
		"https://t.me/c/", "https://t.me/c/nope/41872", "https://t.me/c/0/41872",
		"https://t.me/c/2255983776/0", "https://t.me/group/1/0", "https://t.me/group//1",
		"https://t.me/group/topic/1", "https://t.me/group/1/2/3",
		"https://t.me/group/1?comment=2", "https://t.me/group?start=abc",
		"https://t.me/group/1?thread=0", "https://t.me/group/1?thread=abc",
		"https://t.me/group/1?thread=2&thread=3", "https://t.me/group/1/2?thread=3",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseTagTarget(raw, 0)
			require.Error(t, err)
		})
	}
	_, err := ParseTagTarget("https://t.me/c/2255983776/41872", 500)
	require.ErrorContains(t, err, "conflicts")
	_, err = ParseTagTarget("group", -1)
	require.ErrorContains(t, err, "topic_id")
}

func TestTagHistorySelectsOnlyConfirmedTopic(t *testing.T) {
	for _, topicID := range []int{0, 1, 41872} {
		t.Run(fmt.Sprint(topicID), func(t *testing.T) {
			verified, pages := false, 0
			peer := &tg.InputPeerChannel{ChannelID: 2255983776, AccessHash: 7}
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.MessagesGetForumTopicsByIDRequest:
					require.NotZero(t, topicID)
					require.Equal(t, peer, req.Peer)
					require.Equal(t, []int{topicID}, req.Topics)
					verified = true
					return linkedReply(&tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
						&tg.ForumTopic{ID: topicID, Title: "Topic", Peer: &tg.PeerChannel{ChannelID: peer.ChannelID}, FromID: &tg.PeerChannel{ChannelID: peer.ChannelID}},
					}}, out)
				case *tg.MessagesGetRepliesRequest:
					require.True(t, verified)
					require.Equal(t, topicID, req.MsgID)
					require.Equal(t, peer, req.Peer)
					pages++
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesGetHistoryRequest:
					require.Zero(t, topicID, "topic scans must never fall back to the whole chat")
					pages++
					return linkedReply(&tg.MessagesMessages{}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			history, topic, err := tagHistory(context.Background(), api, peer, topicID)
			require.NoError(t, err)
			if topicID == 0 {
				require.Nil(t, topic)
			} else {
				require.Equal(t, "Topic", topic.Title)
			}
			it := messages.NewIterator(history, 100)
			require.False(t, it.Next(context.Background()))
			require.NoError(t, it.Err())
			require.Equal(t, 1, pages)
		})
	}
}

func TestTagHistoryRefusesOrdinaryMessagesDeletedTopicsAndRPCFailures(t *testing.T) {
	sentinel := errors.New("forum unavailable")
	for _, test := range []struct {
		name   string
		topics []tg.ForumTopicClass
		err    error
	}{
		{name: "ordinary message"},
		{name: "deleted topic", topics: []tg.ForumTopicClass{&tg.ForumTopicDeleted{ID: 41872}}},
		{name: "different topic", topics: []tg.ForumTopicClass{&tg.ForumTopic{ID: 999, Peer: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerChannel{ChannelID: 42}}}},
		{name: "RPC failure", err: sentinel},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				_, ok := in.(*tg.MessagesGetForumTopicsByIDRequest)
				require.True(t, ok, "must refuse before scanning history")
				if test.err != nil {
					return test.err
				}
				return linkedReply(&tg.MessagesForumTopics{Topics: test.topics}, out)
			}))
			_, _, err := tagHistory(context.Background(), api, &tg.InputPeerChannel{ChannelID: 42}, 41872)
			require.Error(t, err)
			if test.err != nil {
				require.ErrorIs(t, err, sentinel)
			} else {
				require.ErrorContains(t, err, "accessible forum topic")
			}
		})
	}
}

func TestTagTopicPaginationKeepsTheSameThread(t *testing.T) {
	peer := &tg.InputPeerChannel{ChannelID: 2255983776, AccessHash: 7}
	pages := 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetForumTopicsByIDRequest:
			return linkedReply(&tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{
				&tg.ForumTopic{ID: 41872, Peer: &tg.PeerChannel{ChannelID: peer.ChannelID}, FromID: &tg.PeerChannel{ChannelID: peer.ChannelID}},
			}}, out)
		case *tg.MessagesGetRepliesRequest:
			pages++
			require.Equal(t, 41872, req.MsgID)
			require.Equal(t, peer, req.Peer)
			require.Equal(t, 100, req.Limit)
			first, last := 42102, 42003
			switch pages {
			case 1:
				require.Zero(t, req.OffsetID)
			case 2:
				require.Equal(t, 2, pages)
				require.Equal(t, 42003, req.OffsetID)
				first, last = 42002, 42001
			default:
				// gotd probes once more after a short final batch.
				require.Equal(t, 3, pages)
				require.Equal(t, 42001, req.OffsetID)
				return linkedReply(&tg.MessagesChannelMessages{Count: 102}, out)
			}
			var batch []tg.MessageClass
			for id := first; id >= last; id-- {
				batch = append(batch, &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: peer.ChannelID}})
			}
			return linkedReply(&tg.MessagesChannelMessages{Count: 102, Messages: batch}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	history, _, err := tagHistory(context.Background(), api, peer, 41872)
	require.NoError(t, err)
	it := messages.NewIterator(history, 100)
	var ids []int
	for it.Next(context.Background()) {
		ids = append(ids, it.Value().Msg.GetID())
	}
	require.NoError(t, it.Err())
	require.Len(t, ids, 102)
	require.Equal(t, 3, pages)
	require.Equal(t, 42102, ids[0])
	require.Equal(t, 42001, ids[101])
}

func TestWholeChatHistoryKeepsMessagesFromDifferentTopics(t *testing.T) {
	peer := &tg.InputPeerChannel{ChannelID: 2255983776, AccessHash: 7}
	pages := 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.MessagesGetHistoryRequest)
		require.True(t, ok, "whole-chat scans must not select a single thread")
		require.Equal(t, peer, req.Peer)
		pages++
		if pages > 1 {
			return linkedReply(&tg.MessagesChannelMessages{Count: 2}, out)
		}
		var batch []tg.MessageClass
		for _, id := range []int{41872, 34506} {
			m := &tg.Message{ID: id + 1, PeerID: &tg.PeerChannel{ChannelID: peer.ChannelID}}
			m.SetReplyTo(&tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: id})
			batch = append(batch, m)
		}
		return linkedReply(&tg.MessagesChannelMessages{Count: 2, Messages: batch}, out)
	}))
	history, topic, err := tagHistory(context.Background(), api, peer, 0)
	require.NoError(t, err)
	require.Nil(t, topic)
	it := messages.NewIterator(history, 100)
	var ids []int
	for it.Next(context.Background()) {
		ids = append(ids, it.Value().Msg.GetID())
	}
	require.NoError(t, it.Err())
	require.Equal(t, []int{41873, 34507}, ids)
}
