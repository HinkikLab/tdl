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

func TestRangeIteratorUsesBoundedRPCAndSharedCompletion(t *testing.T) {
	const end = 10001
	state := NewState()
	state.Skip(1, 500, end, 20000)
	var calls, requested int
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.ChannelsGetMessagesRequest)
		require.True(t, ok)
		require.LessOrEqual(t, len(req.ID), DefaultBatchSize)
		calls++
		var messages []tg.MessageClass
		for _, value := range req.ID {
			id := value.(*tg.InputMessageID).ID
			require.GreaterOrEqual(t, id, 1)
			require.Less(t, id, end)
			require.False(t, state.IsFinished(id))
			requested++
			messages = append(messages, &tg.MessageEmpty{ID: id})
		}
		return encodeResult(&tg.MessagesChannelMessages{Messages: messages}, out)
	}))
	manager, dialog := newIteratorTestChannel(t, api)
	it, err := newIter(batchPool{api}, manager, dialog, t.TempDir(), nil, &iterOptions{
		template: "{{ .DialogID }}_{{ .MessageID }}_{{ .FileName }}", rangeStart: 1, rangeEnd: end, isFinished: state.IsTerminalWithoutMedia,
		onSkip: func(ids []int) { state.Skip(ids...) },
	})
	require.NoError(t, err)
	require.Empty(t, it.ids, "range setup must not materialize IDs")
	require.False(t, it.Next(context.Background()))
	require.NoError(t, it.Err())
	require.Equal(t, 9998, requested)
	require.Equal(t, 100, calls)
	require.Zero(t, countMissingRange(state, 1, end))
	require.Empty(t, it.finished, "the existing State is the completion owner")
}

func newIteratorTestChannel(t *testing.T, api *tg.Client) (*peers.Manager, peers.Peer) {
	t.Helper()
	manager := peers.Options{}.Build(api)
	dialog := manager.Channel(&tg.Channel{ID: 50, AccessHash: 1, Title: "source"})
	return manager, dialog
}

func BenchmarkBatchRangeIterator(b *testing.B) {
	for _, count := range []int{10000, 100000, 1000000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				it, err := newIter(nil, nil, nil, "downloads", nil, &iterOptions{template: "{{ .DialogID }}_{{ .MessageID }}_{{ .FileName }}", rangeStart: 1, rangeEnd: count + 1})
				if err != nil {
					b.Fatal(err)
				}
				rangeBenchmarkSink = it.ids
			}
		})
	}
}
