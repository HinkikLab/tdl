package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestBotUnchangedHistoryBacksOffWithAndWithoutLiveUpdates(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint(live), func(t *testing.T) {
			opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10}
			require.NoError(t, opts.Normalize())
			history := 0
			api := tg.NewClient(linkedRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.MessagesGetHistoryRequest:
					if req.Limit != 1 {
						history++
						deadline, ok := ctx.Deadline()
						require.True(t, ok)
						require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
					}
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesStartBotRequest:
					return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 100}, Message: "No result found for this token"}}}}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			backend := &telegramLinkBackend{api: api, opts: opts}
			if live {
				backend.updates = &BotUpdates{}
			}
			_, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.ErrorContains(t, err, "bot 100")
			require.ErrorContains(t, err, "no actionable resource received")
			require.ErrorContains(t, err, "No result found for this token")
			require.LessOrEqual(t, history, 10, "unchanged history uses exponential backoff rather than 200 polls")
			require.Greater(t, history, 1)
		})
	}
}

func TestBotResponseTimeoutBoundsBlockedHistoryRPCAndKeepsReplySummary(t *testing.T) {
	opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10}
	require.NoError(t, opts.Normalize())
	api := tg.NewClient(linkedRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			if req.Limit == 1 {
				return linkedReply(&tg.MessagesMessages{}, out)
			}
			<-ctx.Done()
			return ctx.Err()
		case *tg.MessagesStartBotRequest:
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 100}, Message: "Token expired"}}}}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	backend := &telegramLinkBackend{api: api, opts: opts}
	started := time.Now()
	_, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "Token expired")
	require.Less(t, time.Since(started), 4*time.Second)
}

func TestBotTimeoutSummaryDistinguishesResourcesAndLimitsText(t *testing.T) {
	replies := map[int]*tg.Message{2: {ID: 2, Message: strings.Repeat("汉", 200)}}
	err := botResponseTimeout(100, 2, replies)
	require.ErrorContains(t, err, "no actionable resource received")
	require.NotContains(t, err.Error(), strings.Repeat("汉", 161))
	replies[3] = linkedDocument(3, 99, "ref", []byte("data"))
	require.ErrorContains(t, botResponseTimeout(100, 2, replies), "resource replies did not settle")
}

func TestBotIdleRequiresFinalHistoryReadAndIncludesLateResource(t *testing.T) {
	opts := LinkOptions{BotTimeout: 4, BotIdle: 1, PollInterval: 10}
	require.NoError(t, opts.Normalize())
	started := time.Time{}
	late := false
	history := 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			if req.Limit == 1 {
				return linkedReply(&tg.MessagesMessages{}, out)
			}
			history++
			if !late && time.Since(started) >= time.Second {
				late = true
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{linkedDocument(3, 100, "ref", []byte("late"))}}, out)
			}
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			started = time.Now()
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: linkedDocument(2, 99, "ref", []byte("first"))}}}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	backend := &telegramLinkBackend{api: api, opts: opts}
	resources, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
	require.NoError(t, err)
	require.Len(t, resources, 2)
	require.True(t, late)
	require.LessOrEqual(t, history, 20)
}
