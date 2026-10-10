package chat

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestBotRequestIntervalUsesLastReplyAcrossJobs(t *testing.T) {
	for _, mode := range []string{"same-bot", "elapsed-during-download", "actual-send-time", "different-bot", "disabled", "edits-and-outgoing"} {
		t.Run(mode, func(t *testing.T) {
			updates := &BotUpdates{}
			previousBot := int64(100)
			if mode == "different-bot" {
				previousBot++
			}
			previous := &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: previousBot}, Message: "最后一条推广文本"}
			switch mode {
			case "elapsed-during-download":
				previous.Date = int(time.Now().Add(-5 * time.Second).Unix())
			case "actual-send-time":
				previous.Date = int(time.Now().Add(-1500 * time.Millisecond).Unix())
			}
			updates.Watch(previousBot, 0, 100)
			require.NoError(t, updates.Handle(t.Context(), &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: previous}}))
			last := updates.lastBotReply(previousBot).sentAt
			updates.Stop(previousBot) // cleanup must preserve timing for later jobs
			opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10, BotRequestInterval: 2}
			if mode == "disabled" {
				opts.BotRequestInterval = 0
			}
			require.NoError(t, opts.Normalize())
			requestTime := time.Time{}
			api := tg.NewClient(linkedRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetHistoryRequest:
					if mode == "edits-and-outgoing" && requestTime.IsZero() {
						edit := *previous
						edit.Message = "edited status"
						edit.SetEditDate(int(time.Now().Unix()))
						require.NoError(t, updates.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
							&tg.UpdateEditMessage{Message: &edit},
							&tg.UpdateNewMessage{Message: &tg.Message{ID: 9, Out: true, PeerID: previous.PeerID, Message: "/start old"}},
						}}))
						return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&edit}}, out)
					}
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesStartBotRequest:
					requestTime = time.Now()
					return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: linkedDocument(20, 99, "fresh", []byte("data"))}}}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			backend := &telegramLinkBackend{api: api, opts: opts, updates: updates}
			began := time.Now()
			result, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
			require.NoError(t, err)
			require.Len(t, result, 1)
			if mode == "same-bot" || mode == "edits-and-outgoing" || mode == "actual-send-time" {
				require.False(t, requestTime.Before(last.Add(2*time.Second)), "measure the interval from the bot's last message")
			}
			if mode == "same-bot" || mode == "edits-and-outgoing" {
				require.Less(t, requestTime.Sub(last), 2500*time.Millisecond)
				require.GreaterOrEqual(t, time.Since(began), 3*time.Second, "the reply timeout starts after the interval wait")
			} else {
				require.Less(t, requestTime.Sub(began), time.Second, "elapsed download time, other bots and zero settings add no full interval")
			}
		})
	}
}

func TestBotRequestIntervalExtendsForLateReplyAndExcludesIt(t *testing.T) {
	updates := &BotUpdates{}
	updates.Watch(100, 0, 100)
	require.NoError(t, updates.Handle(t.Context(), &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 100}, Message: "first reply"}}}))
	opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10, BotRequestInterval: 2}
	require.NoError(t, opts.Normalize())
	watermarkRead := make(chan struct{})
	lateReply := make(chan time.Time, 1)
	lateError := make(chan error, 1)
	timer := time.AfterFunc(150*time.Millisecond, func() {
		<-watermarkRead
		lateError <- updates.Handle(t.Context(), &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: &tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 100}, Message: "late promotion"}}})
		lateReply <- updates.lastBotReply(100).sentAt
	})
	defer timer.Stop()
	requestTime := time.Time{}
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetHistoryRequest:
			if requestTime.IsZero() {
				close(watermarkRead)
			}
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			requestTime = time.Now()
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: linkedDocument(20, 99, "fresh", []byte("data"))}}}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	backend := &telegramLinkBackend{api: api, opts: opts, updates: updates}
	result, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
	require.NoError(t, err)
	require.NoError(t, <-lateError)
	require.False(t, requestTime.Before((<-lateReply).Add(2*time.Second)))
	require.Len(t, result, 1, "late replies from the preceding request cannot mix into the new resource set")
	require.Equal(t, 20, result[0].Message.ID)
}

func TestBotRequestIntervalUsesHistoryWithoutLiveUpdates(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			testBotIntervalHistory(t, late)
		})
	}
}

func testBotIntervalHistory(t *testing.T, late bool) {
	t.Helper()
	opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10, BotRequestInterval: 2}
	require.NoError(t, opts.Normalize())
	sentAt := time.Now().Unix()
	historyReads := 0
	requestTime := time.Time{}
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			if requestTime.IsZero() {
				require.Greater(t, req.Limit, 1, "inspect incoming replies even when the latest message is outgoing")
				historyReads++
				if late && historyReads >= 2 {
					if historyReads == 2 {
						sentAt = time.Now().Unix()
					}
					return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 10, Date: int(sentAt), PeerID: &tg.PeerUser{UserID: 100}, Message: "late last reply"}}}, out)
				}
				return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{
					&tg.Message{ID: 6, Out: true, Date: int(sentAt), PeerID: &tg.PeerUser{UserID: 100}, Message: "/start old"},
					&tg.Message{ID: 5, Date: int(sentAt), PeerID: &tg.PeerUser{UserID: 100}, Message: "last reply"},
				}}, out)
			}
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			requestTime = time.Now()
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: linkedDocument(20, 99, "fresh", []byte("data"))}}}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	backend := &telegramLinkBackend{api: api, opts: opts}
	_, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
	require.NoError(t, err)
	require.False(t, requestTime.Before(time.Unix(sentAt, 0).Add(2*time.Second)))
}

func TestBotRequestIntervalAppliesToFloodRetry(t *testing.T) {
	opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10, BotRequestInterval: 2}
	require.NoError(t, opts.Normalize())
	var requestTimes []time.Time
	sentAt := time.Time{}
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetHistoryRequest:
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			requestTimes = append(requestTimes, time.Now())
			if len(requestTimes) == 1 {
				sentAt = time.Unix(time.Now().Unix(), 0)
				return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 5, Date: int(sentAt.Unix()), PeerID: &tg.PeerUser{UserID: 100}, Message: "Too many requests. Retry after 1 second"}}}}, out)
			}
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: linkedDocument(20, 99, "fresh", []byte("data"))}}}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	backend := &telegramLinkBackend{api: api, opts: opts}
	_, err := backend.requestBot(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
	require.NoError(t, err)
	require.Len(t, requestTimes, 2)
	require.False(t, requestTimes[1].Before(sentAt.Add(2*time.Second)))
}

func TestBotRequestIntervalCancellationDoesNotSend(t *testing.T) {
	opts := LinkOptions{BotTimeout: 2, BotIdle: 1, PollInterval: 10, BotRequestInterval: 30}
	require.NoError(t, opts.Normalize())
	updates := &BotUpdates{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	starts := 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetHistoryRequest:
			cancel()
			return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 100}, Message: "reply"}}}, out)
		case *tg.MessagesStartBotRequest:
			starts++
		}
		return fmt.Errorf("unexpected RPC %T", in)
	}))
	backend := &telegramLinkBackend{api: api, opts: opts, updates: updates}
	_, err := backend.requestBotOnce(ctx, (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, starts)
	require.Empty(t, updates.watches, "release the watch when interval waiting is canceled")
}
