// Package layer adapts the post-init request envelope used by gotd v0.140.0.
package layer

import (
	"context"
	"encoding/binary"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// New restores the ordinary RPC envelope used before gotd v0.135.0.
//
// invokeWithLayer must accompany initConnection. gotd v0.140.0 also adds it
// to every subsequent RPC, which can make a mutation fail with
// CONNECTION_LAYER_INVALID and leave following calls in the same bad state.
// See https://core.telegram.org/api/invoking#layers.
//
// The connection manager adds this envelope after invoking middleware. Its
// public API offers no way to change it, so the encoder removes only that
// exact generated prefix. Connection initialization invokes MTProto directly
// and remains untouched. This adapter can be removed when gotd restores the
// ordinary request envelope; a request with no matching prefix passes through.
func New() telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			// Preserve explicitly requested connection initialization and layers.
			switch input.(type) {
			case *tg.InitConnectionRequest, *tg.InvokeWithLayerRequest, encoder:
				return next.Invoke(ctx, input, output)
			}
			return next.Invoke(ctx, encoder{input}, output)
		}
	})
}

type encoder struct{ bin.Encoder }

func (e encoder) Encode(b *bin.Buffer) error {
	// Data mode adds withoutUpdates(withLayer(withoutUpdates(query))).
	// Retain a single withoutUpdates to keep file/control pool connections
	// unsubscribed from updates, as required by their connection mode.
	if n := b.Len(); n >= 16 &&
		id(b.Buf[n-16:]) == tg.InvokeWithoutUpdatesRequestTypeID &&
		id(b.Buf[n-12:]) == tg.InvokeWithLayerRequestTypeID &&
		id(b.Buf[n-8:]) == uint32(tg.Layer) &&
		id(b.Buf[n-4:]) == tg.InvokeWithoutUpdatesRequestTypeID {
		b.Buf = b.Buf[:n-12]
	} else if n >= 8 &&
		id(b.Buf[n-8:]) == tg.InvokeWithLayerRequestTypeID &&
		id(b.Buf[n-4:]) == uint32(tg.Layer) {
		// Updates mode adds withLayer(query).
		b.Buf = b.Buf[:n-8]
	}
	return e.Encoder.Encode(b)
}

func id(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
