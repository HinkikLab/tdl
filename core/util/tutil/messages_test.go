package tutil

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type messagesRPC func(context.Context, bin.Encoder, bin.Decoder) error

func (f messagesRPC) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return f(ctx, input, output)
}

func messageResult(input bin.Encoder, output bin.Decoder) error {
	var body bin.Buffer
	if err := input.Encode(&body); err != nil {
		return err
	}
	return output.Decode(&body)
}

func TestGetMessagesUsesPeerSpecificRPC(t *testing.T) {
	for _, tc := range []struct {
		name    string
		peer    tg.InputPeerClass
		actual  tg.PeerClass
		channel bool
		slice   bool
	}{
		{name: "channel", peer: &tg.InputPeerChannel{ChannelID: 50, AccessHash: 99}, actual: &tg.PeerChannel{ChannelID: 50}, channel: true},
		{name: "basic chat", peer: &tg.InputPeerChat{ChatID: 50}, actual: &tg.PeerChat{ChatID: 50}},
		{name: "user", peer: &tg.InputPeerUser{UserID: 50, AccessHash: 99}, actual: &tg.PeerUser{UserID: 50}, slice: true},
		{name: "self", peer: &tg.InputPeerSelf{}, actual: &tg.PeerUser{UserID: 50}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := tg.NewClient(messagesRPC(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
				calls++
				var ids []tg.InputMessageClass
				if tc.channel {
					req, ok := input.(*tg.ChannelsGetMessagesRequest)
					require.True(t, ok)
					require.Equal(t, &tg.InputChannel{ChannelID: 50, AccessHash: 99}, req.Channel)
					ids = req.ID
				} else {
					req, ok := input.(*tg.MessagesGetMessagesRequest)
					require.True(t, ok)
					ids = req.ID
				}
				var values []int
				for _, id := range ids {
					values = append(values, id.(*tg.InputMessageID).ID)
				}
				require.Equal(t, []int{9, 7, 3}, values)
				messages := []tg.MessageClass{&tg.Message{ID: 9, PeerID: tc.actual}, &tg.MessageEmpty{ID: 7}, &tg.Message{ID: 99, PeerID: tc.actual}}
				if tc.channel {
					return messageResult(&tg.MessagesChannelMessages{Messages: messages}, output)
				}
				if tc.slice {
					return messageResult(&tg.MessagesMessagesSlice{Count: len(messages), Messages: messages}, output)
				}
				return messageResult(&tg.MessagesMessages{Messages: messages}, output)
			}))
			found, missing, err := GetMessages(context.Background(), client, tc.peer, []int{3, 9, 7, 3})
			require.NoError(t, err)
			require.Len(t, found, 1, "unsolicited IDs are not discovered work")
			require.Equal(t, 9, found[9].ID)
			require.Equal(t, []int{3, 7}, missing)
			require.Equal(t, 1, calls, "one metadata RPC per batch for every supported peer")
		})
	}
}

func TestGetMessagesRejectsWrongPeer(t *testing.T) {
	for _, actual := range []tg.PeerClass{&tg.PeerChat{ChatID: 60}, &tg.PeerChannel{ChannelID: 50}, &tg.PeerUser{UserID: 50}} {
		client := tg.NewClient(messagesRPC(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			return messageResult(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: actual}}}, output)
		}))
		_, _, err := GetMessages(context.Background(), client, &tg.InputPeerChat{ChatID: 50}, []int{7})
		require.ErrorContains(t, err, "different peer")
	}
}

func TestGetMessagesValidatesBeforeRPC(t *testing.T) {
	client := tg.NewClient(messagesRPC(func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("invalid request must not issue an RPC")
		return nil
	}))
	for _, ids := range [][]int{{0}, {-1}, {7, 0}} {
		_, _, err := GetMessages(context.Background(), client, &tg.InputPeerChat{ChatID: 50}, ids)
		require.Error(t, err)
	}
	for _, peer := range []tg.InputPeerClass{nil, &tg.InputPeerEmpty{}} {
		_, _, err := GetMessages(context.Background(), client, peer, []int{7})
		require.ErrorContains(t, err, "unsupported")
	}
	found, missing, err := GetMessages(context.Background(), nil, nil, nil)
	require.NoError(t, err)
	require.Empty(t, found)
	require.Empty(t, missing)
}

func TestGetMessagesPropagatesRPCAndUnsupportedResponse(t *testing.T) {
	expected := errors.New("server unavailable")
	client := tg.NewClient(messagesRPC(func(context.Context, bin.Encoder, bin.Decoder) error { return expected }))
	_, _, err := GetMessages(context.Background(), client, &tg.InputPeerChat{ChatID: 50}, []int{7})
	require.ErrorIs(t, err, expected)
	client = tg.NewClient(messagesRPC(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		return messageResult(&tg.MessagesMessagesNotModified{}, output)
	}))
	_, _, err = GetMessages(context.Background(), client, &tg.InputPeerChat{ChatID: 50}, []int{7})
	require.ErrorContains(t, err, "unexpected messages type")
}

func TestGetGroupedMessagesRejectsPartialAlbumOnPaginationError(t *testing.T) {
	for _, expected := range []error{errors.New("history page unavailable"), context.Canceled} {
		t.Run(expected.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root := &tg.Message{ID: 100, PeerID: &tg.PeerChannel{ChannelID: 50}}
			root.SetGroupedID(77)
			page := []tg.MessageClass{root}
			// Deleted placeholders make a full RPC page while the iterator has
			// only one ordinary message to yield before requesting the next page.
			for id := 99; id >= 81; id-- {
				page = append(page, &tg.MessageEmpty{ID: id})
			}
			calls := 0
			client := tg.NewClient(messagesRPC(func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
				calls++
				request := input.(*tg.MessagesGetHistoryRequest)
				require.Equal(t, 20, request.Limit)
				if calls == 1 {
					require.Equal(t, 111, request.OffsetID)
					return messageResult(&tg.MessagesMessagesSlice{Count: 21, Messages: page}, output)
				}
				require.Equal(t, 81, request.OffsetID)
				if errors.Is(expected, context.Canceled) {
					cancel()
					return ctx.Err()
				}
				return expected
			}))
			album, err := GetGroupedMessages(ctx, client, &tg.InputPeerChannel{ChannelID: 50}, root)
			require.ErrorIs(t, err, expected)
			require.Nil(t, album, "a partial album must never look like a complete selection")
			require.Equal(t, 2, calls)
		})
	}
}
