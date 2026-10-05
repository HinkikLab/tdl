package tclient

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/middlewares/takeout"
)

type layerObject struct{ bin.Encoder }

func (layerObject) Decode(*bin.Buffer) error { panic("encoder only") }

func TestClientMiddlewaresCorrectPostInitLayer(t *testing.T) {
	ctx := logctx.With(context.Background(), zap.NewNop())
	for _, data := range []bool{false, true} {
		var invoker tg.Invoker = telegram.InvokeFunc(func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
			var query bin.Object = layerObject{input}
			if data {
				query = &tg.InvokeWithoutUpdatesRequest{Query: query}
			}
			var request bin.Object = &tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: query}
			if data {
				request = &tg.InvokeWithoutUpdatesRequest{Query: request}
			}
			b := &bin.Buffer{}
			require.NoError(t, request.Encode(b))
			if data {
				require.NoError(t, b.ConsumeID(tg.InvokeWithoutUpdatesRequestTypeID))
			}
			require.NoError(t, b.ConsumeID(tg.InvokeWithTakeoutRequestTypeID))
			id, err := b.Long()
			require.NoError(t, err)
			require.Equal(t, int64(567), id)
			require.NoError(t, b.ConsumeID(tg.MessagesDeleteMessagesRequestTypeID))
			return nil
		})
		chain := clientMiddlewares(ctx, time.Second, []telegram.Middleware{takeout.Middleware(567)})
		for i := len(chain) - 1; i >= 0; i-- {
			invoker = chain[i].Handle(invoker)
		}
		require.NoError(t, invoker.Invoke(ctx, &tg.MessagesDeleteMessagesRequest{ID: []int{101}}, &tg.MessagesAffectedMessages{}))
	}
}
