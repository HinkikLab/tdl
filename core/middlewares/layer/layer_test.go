package layer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"
)

type object struct{ bin.Encoder }

func (object) Decode(*bin.Buffer) error { return fmt.Errorf("decode unavailable") }

// gotdEnvelope uses the generated TL objects and exact manager.Conn.Invoke
// composition from the pinned dependency, including its double data wrapper.
func gotdEnvelope(input bin.Encoder, data bool) bin.Encoder {
	var q bin.Object = object{input}
	if data {
		q = &tg.InvokeWithoutUpdatesRequest{Query: q}
	}
	var request bin.Object = &tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: q}
	if data {
		request = &tg.InvokeWithoutUpdatesRequest{Query: request}
	}
	return request
}

func startBot() *tg.MessagesStartBotRequest {
	return &tg.MessagesStartBotRequest{
		Bot:        &tg.InputUser{UserID: 23, AccessHash: 456},
		Peer:       &tg.InputPeerUser{UserID: 23, AccessHash: 456},
		RandomID:   -7,
		StartParam: "resource_abc",
	}
}

func TestNormalRPCEnvelope(t *testing.T) {
	requests := []bin.Encoder{
		startBot(),
		&tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: 23, AccessHash: 456}, Limit: 1},
		&tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{101, 102}},
		&tg.UploadGetFileRequest{Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{3}}, Limit: 512 * 1024},
		&tg.InvokeWithTakeoutRequest{TakeoutID: 567, Query: object{startBot()}},
		&tg.InvokeAfterMsgRequest{MsgID: 987, Query: object{startBot()}},
	}
	for _, data := range []bool{false, true} {
		for _, request := range requests {
			t.Run(fmt.Sprintf("data=%v/%T", data, request), func(t *testing.T) {
				expected := &bin.Buffer{}
				if data {
					expected.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
				}
				require.NoError(t, request.Encode(expected))
				// MTProto may put the API encoder after another transport header.
				prefix := []byte{21, 22, 23, 24, 25, 26, 27, 28}
				got := &bin.Buffer{Buf: append([]byte(nil), prefix...)}
				require.NoError(t, gotdEnvelope(encoder{request}, data).Encode(got))
				require.Equal(t, append(prefix, expected.Buf...), got.Buf)
			})
		}
	}
}

func TestMTProtoMessageHeaderAndLengthArePreserved(t *testing.T) {
	for _, data := range []bool{false, true} {
		payload := &bin.Buffer{}
		if data {
			payload.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
		}
		require.NoError(t, startBot().Encode(payload))
		// Exercise gotd's no-compression path: the API encoder runs after
		// salt/session/message/seqno/length fields in the same buffer.
		message := crypto.EncryptedMessageData{Salt: 123, SessionID: 456, MessageID: 789, SeqNo: 3, Message: gotdEnvelope(encoder{startBot()}, data)}
		b := &bin.Buffer{}
		require.NoError(t, message.EncodeWithoutCopy(b))
		var decoded crypto.EncryptedMessageData
		require.NoError(t, decoded.Decode(b))
		require.Equal(t, message.Salt, decoded.Salt)
		require.Equal(t, message.SessionID, decoded.SessionID)
		require.Equal(t, message.MessageID, decoded.MessageID)
		require.Equal(t, message.SeqNo, decoded.SeqNo)
		require.Equal(t, int32(payload.Len()), decoded.MessageDataLen)
		require.Equal(t, payload.Buf, decoded.Data())
	}
}

func TestBotLayerErrorIsFixedWithoutRetry(t *testing.T) {
	for _, data := range []bool{false, true} {
		t.Run(fmt.Sprintf("data=%v", data), func(t *testing.T) {
			calls := 0
			connection := telegram.InvokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				wire := &bin.Buffer{}
				if err := gotdEnvelope(in, data).Encode(wire); err != nil {
					return err
				}
				if data {
					require.NoError(t, wire.ConsumeID(tg.InvokeWithoutUpdatesRequestTypeID))
				}
				if id(wire.Buf) == tg.InvokeWithLayerRequestTypeID {
					return &tgerr.Error{Code: 400, Type: "CONNECTION_LAYER_INVALID"}
				}
				var decoded tg.MessagesStartBotRequest
				require.NoError(t, decoded.Decode(wire))
				require.Equal(t, startBot(), &decoded)
				require.Zero(t, wire.Len())
				return nil
			})
			ctx := context.Background()
			require.True(t, tgerr.Is(connection.Invoke(ctx, startBot(), nil), "CONNECTION_LAYER_INVALID"))
			require.NoError(t, New().Handle(connection).Invoke(ctx, startBot(), nil))
			require.Equal(t, 2, calls, "one baseline call and one corrected call; no retry or second mutation")
		})
	}
}

func TestExplicitInitAndLayerPreserved(t *testing.T) {
	requests := []bin.Encoder{
		&tg.InitConnectionRequest{APIID: 123, DeviceModel: "test", SystemVersion: "test", AppVersion: "test", LangCode: "en", SystemLangCode: "en", Query: &tg.HelpGetConfigRequest{}},
		&tg.InvokeWithLayerRequest{Layer: tg.Layer, Query: &tg.HelpGetConfigRequest{}},
	}
	for _, request := range requests {
		for _, data := range []bool{false, true} {
			next := telegram.InvokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
				require.Same(t, request, in)
				actual, expected := &bin.Buffer{}, &bin.Buffer{}
				require.NoError(t, gotdEnvelope(in, data).Encode(actual))
				require.NoError(t, gotdEnvelope(request, data).Encode(expected))
				require.Equal(t, expected.Buf, actual.Buf)
				return nil
			})
			require.NoError(t, New().Handle(next).Invoke(context.Background(), request, nil))
		}
	}
}

func TestUnknownPrefixAndLayerPassThrough(t *testing.T) {
	for _, prefix := range [][]byte{{}, {1, 2, 3, 4}, {1, 2, 3, 4, 5, 6, 7, 8}} {
		b := &bin.Buffer{Buf: append([]byte(nil), prefix...)}
		expected := &bin.Buffer{Buf: append([]byte(nil), prefix...)}
		require.NoError(t, startBot().Encode(expected))
		require.NoError(t, (encoder{startBot()}).Encode(b))
		require.Equal(t, expected.Buf, b.Buf)
	}
	request := &tg.InvokeWithLayerRequest{Layer: tg.Layer + 1, Query: object{encoder{startBot()}}}
	actual, expected := &bin.Buffer{}, &bin.Buffer{}
	baseline := &tg.InvokeWithLayerRequest{Layer: tg.Layer + 1, Query: startBot()}
	require.NoError(t, request.Encode(actual))
	require.NoError(t, baseline.Encode(expected))
	require.Equal(t, expected.Buf, actual.Buf, "do not guess a changed dependency envelope")
}

func TestMiddlewarePreservesContextOutputAndError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := &tg.UpdatesBox{}
	wantErr := errors.New("network failure")
	next := telegram.InvokeFunc(func(got context.Context, input bin.Encoder, out bin.Decoder) error {
		require.Same(t, ctx, got)
		require.Same(t, output, out)
		require.IsType(t, encoder{}, input)
		return wantErr
	})
	err := New().Handle(next).Invoke(ctx, startBot(), output)
	require.Same(t, wantErr, err)
}

func TestConcurrentEncodingAndRepeatedMiddleware(t *testing.T) {
	expected := &bin.Buffer{}
	require.NoError(t, startBot().Encode(expected))
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := telegram.InvokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
				b := &bin.Buffer{}
				if err := gotdEnvelope(in, false).Encode(b); err != nil {
					return err
				}
				if !bytes.Equal(expected.Buf, b.Buf) {
					return fmt.Errorf("payload changed")
				}
				return nil
			})
			require.NoError(t, New().Handle(New().Handle(next)).Invoke(context.Background(), startBot(), nil))
		}()
	}
	wg.Wait()
}
