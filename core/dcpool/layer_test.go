package dcpool

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/middlewares/takeout"
)

type encoderObject struct{ bin.Encoder }

func (encoderObject) Decode(*bin.Buffer) error { return fmt.Errorf("encoder only") }

func TestPoolAlwaysCorrectsFinalEnvelope(t *testing.T) {
	p := NewPool(nil, 1, takeout.Middleware(123)).(*pool)
	connection := telegram.InvokeFunc(func(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
		// The real pooled connection adds these wrappers after all middleware.
		request := &tg.InvokeWithoutUpdatesRequest{Query: &tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: &tg.InvokeWithoutUpdatesRequest{Query: encoderObject{input}}}}
		b := &bin.Buffer{}
		require.NoError(t, request.Encode(b))
		require.NoError(t, b.ConsumeID(tg.InvokeWithoutUpdatesRequestTypeID))
		require.NoError(t, b.ConsumeID(tg.InvokeWithTakeoutRequestTypeID))
		id, err := b.Long()
		require.NoError(t, err)
		require.Equal(t, int64(123), id)
		require.NoError(t, b.ConsumeID(tg.MessagesDeleteMessagesRequestTypeID))
		return nil
	})
	err := chainMiddlewares(connection, p.middlewares...).Invoke(context.Background(), &tg.MessagesDeleteMessagesRequest{ID: []int{101}}, &tg.MessagesAffectedMessages{})
	require.NoError(t, err)
}
