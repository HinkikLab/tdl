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

	"github.com/iyear/tdl/core/tmedia"
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

func botMultipleReplies(ref string) []*tg.Message {
	video := linkedDocument(4, 99, ref, []byte("video"))
	video.Media.(*tg.MessageMediaDocument).Video = true
	photo := &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 100}, Date: 1700000000}
	photo.SetMedia(&tg.MessageMediaPhoto{Photo: &tg.Photo{
		ID: 100, AccessHash: 55, DCID: 2, FileReference: []byte(ref),
		Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 100, H: 100, Size: 5}},
	}})
	return []*tg.Message{
		{ID: 2, PeerID: &tg.PeerUser{UserID: 100}, Message: "正在获取资源"},
		{ID: 3, PeerID: &tg.PeerUser{UserID: 100}, Message: "🔐 检测到此分享码共2个资源 ⏰ 资源10分钟后自动删除(可重复获取)"},
		video, photo,
		{ID: 6, PeerID: &tg.PeerUser{UserID: 100}, Message: "推广链接 https://t.me/promotion_bot?start=promo"},
	}
}

func TestBotMultipleRepliesSettleAcrossLiveAndHistoryMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		live    bool
		deleted bool
	}{
		{name: "history"},
		{name: "live-and-history", live: true},
		{name: "deleted-after-history-refresh", live: true, deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := LinkOptions{BotTimeout: 3, BotIdle: 1, PollInterval: 10}
			require.NoError(t, opts.Normalize())
			var updates *BotUpdates
			if tc.live {
				updates = &BotUpdates{}
			}
			history := 0
			latestReference := ""
			api := tg.NewClient(linkedRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.MessagesGetHistoryRequest:
					if req.Limit == 1 {
						return linkedReply(&tg.MessagesMessages{}, out)
					}
					history++
					if tc.deleted && history > 1 {
						return linkedReply(&tg.MessagesMessages{}, out)
					}
					latestReference = fmt.Sprintf("history-%d", history)
					var replies []tg.MessageClass
					for _, m := range botMultipleReplies(latestReference) {
						replies = append(replies, m)
					}
					return linkedReply(&tg.MessagesMessages{Messages: replies}, out)
				case *tg.MessagesStartBotRequest:
					var replies []tg.UpdateClass
					for _, m := range botMultipleReplies("live") {
						m.MediaUnread = true
						replies = append(replies, &tg.UpdateNewMessage{Message: m})
					}
					if updates != nil {
						require.NoError(t, updates.Handle(ctx, &tg.Updates{Updates: replies}))
					}
					return linkedReply(&tg.Updates{Updates: replies}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			backend := &telegramLinkBackend{api: api, opts: opts, updates: updates}
			started := time.Now()
			result, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
			require.NoError(t, err)
			require.Len(t, result, 5)
			require.GreaterOrEqual(t, time.Since(started), time.Second)
			require.Less(t, time.Since(started), 3*time.Second)
			for i, reply := range result {
				require.Equal(t, i+2, reply.Message.ID)
			}
			for _, i := range []int{2, 3} {
				md, ok := tmedia.GetMedia(result[i].Message)
				require.True(t, ok)
				require.Equal(t, latestReference, string(mediaReference(md)), "retain the final history reference rather than the cached live update")
			}
			require.LessOrEqual(t, history, 12)
		})
	}
}

func TestBotEditedReplyWaitsForIdleAndIgnoresStaleCopies(t *testing.T) {
	for _, source := range []string{"live", "history"} {
		t.Run(source, func(t *testing.T) {
			opts := LinkOptions{BotTimeout: 4, BotIdle: 1, PollInterval: 10}
			require.NoError(t, opts.Normalize())
			updates := &BotUpdates{}
			started, edited := time.Time{}, time.Time{}
			fresh := func() *tg.Message {
				m := linkedDocument(2, 100, "edited", []byte("new data"))
				m.Message = "已更新 https://t.me/files_bot?start=edited"
				m.SetEditDate(m.Date + 1)
				return m
			}
			api := tg.NewClient(linkedRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.MessagesStartBotRequest:
					started = time.Now()
					m := linkedDocument(2, 99, "original", []byte("original data"))
					require.NoError(t, updates.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: m}}}))
					return linkedReply(&tg.Updates{}, out)
				case *tg.MessagesGetHistoryRequest:
					if req.Limit == 1 {
						return linkedReply(&tg.MessagesMessages{}, out)
					}
					if time.Since(started) >= 500*time.Millisecond {
						if edited.IsZero() {
							edited = time.Now()
							if source == "live" {
								require.NoError(t, updates.Handle(ctx, &tg.UpdateShort{Update: &tg.UpdateEditMessage{Message: fresh()}}))
							}
						}
						if source == "history" {
							return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{fresh()}}, out)
						}
					}
					// A cached history response must not roll back a newer live edit.
					return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{linkedDocument(2, 99, "original", []byte("original data"))}}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			backend := &telegramLinkBackend{api: api, opts: opts, updates: updates}
			result, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
			require.NoError(t, err)
			require.Len(t, result, 1)
			require.False(t, edited.IsZero())
			require.GreaterOrEqual(t, time.Since(edited), time.Second, "a genuine edit restarts the idle interval")
			require.Equal(t, fresh().Message, result[0].Message.Message)
			md, ok := tmedia.GetMedia(result[0].Message)
			require.True(t, ok)
			require.Equal(t, "document_100", resourceIdentity(md))
			require.Equal(t, "edited", string(mediaReference(md)))
		})
	}
}

func TestBotTimeoutSummaryIncludesFinalLinkedText(t *testing.T) {
	replies := map[int]*tg.Message{}
	for _, m := range botMultipleReplies("ref") {
		replies[m.ID] = m
	}
	err := botResponseTimeout(100, 120, replies)
	require.ErrorContains(t, err, "(5 messages)")
	require.ErrorContains(t, err, "last reply (ID 6)")
	require.ErrorContains(t, err, "推广链接 https://t.me/promotion_bot?start=promo")
	require.NotContains(t, err.Error(), "last non-resource reply")
	delete(replies, 6)
	err = botResponseTimeout(100, 120, replies)
	require.ErrorContains(t, err, "last reply (ID 5)")
	require.ErrorContains(t, err, "messageMediaPhoto")
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

func TestBotLateLiveRepliesDuringFinalHistoryReadAreCollected(t *testing.T) {
	opts := LinkOptions{BotTimeout: 4, BotIdle: 1, PollInterval: 10}
	require.NoError(t, opts.Normalize())
	updates := &BotUpdates{}
	started, late := time.Time{}, time.Time{}
	api := tg.NewClient(linkedRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.MessagesGetHistoryRequest:
			if req.Limit != 1 && late.IsZero() && time.Since(started) >= time.Second {
				late = time.Now()
				// These replies arrive on the update stream while the final RPC is
				// in flight, then disappear before history can return them.
				require.NoError(t, updates.Handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{
					&tg.UpdateNewMessage{Message: linkedDocument(3, 100, "late", []byte("late"))},
					&tg.UpdateNewMessage{Message: &tg.Message{ID: 4, PeerID: &tg.PeerUser{UserID: 100}, Message: "推广链接 https://t.me/promotion_bot?start=promo"}},
					&tg.UpdateDeleteMessages{Messages: []int{3, 4}},
				}}))
			}
			return linkedReply(&tg.MessagesMessages{}, out)
		case *tg.MessagesStartBotRequest:
			started = time.Now()
			return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: linkedDocument(2, 99, "first", []byte("first"))}}}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	backend := &telegramLinkBackend{api: api, opts: opts, updates: updates}
	result, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
	require.NoError(t, err)
	require.Len(t, result, 3)
	require.Equal(t, []int{2, 3, 4}, []int{result[0].Message.ID, result[1].Message.ID, result[2].Message.ID})
	require.GreaterOrEqual(t, time.Since(late), time.Second, "late live replies restart the idle interval")
}

func TestBotResponseLimitAndCancellationWithCollectedResources(t *testing.T) {
	for _, stage := range []string{"limit", "cancel", "cancel-at-idle"} {
		t.Run(stage, func(t *testing.T) {
			opts := LinkOptions{BotTimeout: 3, BotIdle: 1, PollInterval: 10, MaxBotMessages: 4}
			require.NoError(t, opts.Normalize())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := time.Time{}
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.MessagesGetHistoryRequest:
					if req.Limit != 1 && (stage == "cancel" || stage == "cancel-at-idle" && time.Since(started) >= time.Second) {
						cancel()
					}
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesStartBotRequest:
					started = time.Now()
					var replies []tg.UpdateClass
					if stage == "limit" {
						for _, m := range botMultipleReplies("ref") {
							replies = append(replies, &tg.UpdateNewMessage{Message: m})
						}
					} else {
						replies = append(replies, &tg.UpdateNewMessage{Message: linkedDocument(2, 99, "ref", []byte("data"))})
					}
					return linkedReply(&tg.Updates{Updates: replies}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			backend := &telegramLinkBackend{api: api, opts: opts}
			_, err := backend.requestBotOnce(ctx, (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
			if stage == "limit" {
				require.ErrorContains(t, err, "max_bot_messages")
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}
