package chat

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"
)

func TestBotRPCFailuresIdentifyOperationAndPreserveRPCError(t *testing.T) {
	for _, stage := range []string{"watermark", "start", "history", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			rpcErr := tgerr.New(400, "CONNECTION_LAYER_INVALID")
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetHistoryRequest:
					if stage == "watermark" || stage == "history" {
						return rpcErr
					}
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesStartBotRequest:
					if stage == "start" {
						return rpcErr
					}
				case *tg.MessagesDeleteMessagesRequest:
					if stage == "cleanup" {
						return rpcErr
					}
				}
				return fmt.Errorf("unexpected RPC %T", in)
			}))
			backend := &telegramLinkBackend{api: api}
			require.NoError(t, backend.opts.Normalize())
			var err error
			switch stage {
			case "cleanup":
				backend.cleanupIDs = map[int]bool{7: true}
				err = backend.Cleanup(t.Context())
			case "history":
				_, err = backend.historySince(t.Context(), &tg.InputPeerUser{UserID: 100}, 7)
			default:
				bot := (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true})
				_, err = backend.requestBotOnce(t.Context(), bot, "fixture")
			}
			require.Error(t, err)
			require.True(t, tgerr.Is(err, "CONNECTION_LAYER_INVALID"))
			switch stage {
			case "cleanup":
				require.ErrorContains(t, err, "delete bot archive messages")
			case "history":
				require.ErrorContains(t, err, "response history")
			case "watermark":
				require.ErrorContains(t, err, "request watermark")
			case "start":
				require.ErrorContains(t, err, "start bot 100")
			}
		})
	}
}
