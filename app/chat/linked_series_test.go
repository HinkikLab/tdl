package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"
)

const seriesStart = 1700000000

type seriesItem struct {
	id, from int
	group    int64
	date     int
	text     string
	media    bool
	service  bool
}

func seriesMessage(item seriesItem) tg.MessageClass {
	if item.service {
		return &tg.MessageService{ID: item.id, PeerID: &tg.PeerChannel{ChannelID: 60}, Action: &tg.MessageActionPinMessage{}}
	}
	m := &tg.Message{ID: item.id, PeerID: &tg.PeerChannel{ChannelID: 60}, Message: item.text, Date: seriesStart + item.date}
	if item.media {
		m = linkedDocument(item.id, int64(1000+item.id), "ref", []byte("data"))
		m.PeerID, m.Message, m.Date = &tg.PeerChannel{ChannelID: 60}, item.text, seriesStart+item.date
	}
	m.SetFromID(&tg.PeerUser{UserID: int64(item.from)})
	if item.group != 0 {
		m.SetGroupedID(item.group)
	}
	return m
}

// seriesAPI serves a supergroup whose message IDs are sequential, as Telegram does.
func seriesAPI(items []seriesItem, lookups *int) *tg.Client {
	store := map[int]tg.MessageClass{}
	for _, item := range items {
		store[item.id] = seriesMessage(item)
	}
	return tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ChannelsGetMessagesRequest:
			if len(req.ID) > 1 && lookups != nil {
				*lookups++
			}
			var msgs []tg.MessageClass
			for _, raw := range req.ID {
				id := raw.(*tg.InputMessageID).ID
				if m := store[id]; m != nil {
					msgs = append(msgs, m)
				} else {
					msgs = append(msgs, &tg.MessageEmpty{ID: id})
				}
			}
			return linkedReply(&tg.MessagesChannelMessages{Messages: msgs}, out)
		case *tg.MessagesGetHistoryRequest:
			var ids []int
			for id := range store {
				if id < req.OffsetID {
					ids = append(ids, id)
				}
			}
			sort.Sort(sort.Reverse(sort.IntSlice(ids)))
			var msgs []tg.MessageClass
			for _, id := range ids[:min(len(ids), req.Limit)] {
				msgs = append(msgs, store[id])
			}
			return linkedReply(&tg.MessagesMessages{Messages: msgs}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
}

func seriesIDs(msgs []resourceMessage) []int {
	ids := make([]int, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.Message.ID)
	}
	return ids
}

func TestLinkedMessageCollectsFollowUpAlbumsFromSameSender(t *testing.T) {
	base := func() []seriesItem {
		return []seriesItem{
			{id: 9, from: 7, date: -10, text: "earlier post", media: true},
			{id: 10, from: 7, group: 1, text: "Pack A (1/3)", media: true},
			{id: 11, from: 7, group: 1, media: true},
			{id: 12, from: 7, group: 1, media: true},
			{id: 13, from: 8, date: 5, text: "nice"},
			{id: 14, from: 7, group: 2, date: 20, media: true},
			{id: 15, from: 7, group: 2, date: 20, media: true},
			{id: 16, service: true},
			{id: 17, from: 7, group: 3, date: 40, text: "Pack A (3/3)", media: true},
			{id: 18, from: 7, group: 3, date: 40, media: true},
			{id: 19, from: 7, date: 50, text: "Pack B", media: true},
			{id: 20, from: 7, date: 55, media: true},
		}
	}
	for _, tc := range []struct {
		name   string
		change func([]seriesItem) []seriesItem
		single bool
		off    bool
		want   []int
	}{
		{name: "albums across interleaved messages until a new caption", want: []int{10, 11, 12, 14, 15, 17, 18}},
		{name: "gap ends the series", change: func(items []seriesItem) []seriesItem {
			items[8].date, items[9].date = 200, 200
			return items
		}, want: []int{10, 11, 12, 14, 15}},
		{name: "text from the sender ends the series", change: func(items []seriesItem) []seriesItem {
			items[7] = seriesItem{id: 16, from: 7, date: 30, text: "next one"}
			return items
		}, want: []int{10, 11, 12, 14, 15}},
		{name: "resource links end the series", change: func(items []seriesItem) []seriesItem {
			items[5].text = "more https://t.me/other/5"
			return items
		}, want: []int{10, 11, 12}},
		{name: "single link", single: true, want: []int{10}},
		{name: "disabled", off: true, want: []int{10, 11, 12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := base()
			if tc.change != nil {
				items = tc.change(items)
			}
			opts := LinkOptions{}
			if tc.off {
				no := false
				opts.FollowSeries = &no
			}
			require.NoError(t, opts.Normalize())
			b := &telegramLinkBackend{api: seriesAPI(items, nil), opts: opts}
			msgs, err := b.resourceMessages(t.Context(), &tg.InputPeerChannel{ChannelID: 60, AccessHash: 600}, 10, tc.single, 0)
			require.NoError(t, err)
			require.Equal(t, tc.want, seriesIDs(msgs))
		})
	}
}

func TestLinkedSeriesStopsAtTheNewestMessageAndScanBound(t *testing.T) {
	items := []seriesItem{{id: 10, from: 7, text: "Pack", media: true}, {id: 11, from: 7, media: true}}
	lookups := 0
	b := &telegramLinkBackend{api: seriesAPI(items, &lookups), opts: LinkOptions{}}
	msgs, err := b.resourceMessages(t.Context(), &tg.InputPeerChannel{ChannelID: 60, AccessHash: 600}, 10, false, 0)
	require.NoError(t, err)
	require.Equal(t, []int{10, 11}, seriesIDs(msgs))
	require.Equal(t, 2, lookups, "an empty page past the newest message ends the scan")

	// A busy group where other members keep posting within the gap.
	items = []seriesItem{{id: 10, from: 7, text: "Pack", media: true}}
	for id := 11; id < 11+2*maxSeriesScan; id++ {
		items = append(items, seriesItem{id: id, from: 8, text: "chat"})
	}
	lookups = 0
	b = &telegramLinkBackend{api: seriesAPI(items, &lookups), opts: LinkOptions{}}
	msgs, err = b.resourceMessages(t.Context(), &tg.InputPeerChannel{ChannelID: 60, AccessHash: 600}, 10, false, 0)
	require.NoError(t, err)
	require.Equal(t, []int{10}, seriesIDs(msgs))
	require.Equal(t, maxSeriesScan/seriesPage, lookups)
}

func TestLinkedSeriesStaysInTheLinkedTopic(t *testing.T) {
	topic := func(item seriesItem, topicID int) tg.MessageClass {
		m := seriesMessage(item).(*tg.Message)
		m.SetReplyTo(&tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: topicID})
		return m
	}
	store := map[int]tg.MessageClass{
		10: topic(seriesItem{id: 10, from: 7, text: "Pack", media: true}, 5),
		11: topic(seriesItem{id: 11, from: 7, media: true}, 6),
		12: topic(seriesItem{id: 12, from: 7, media: true}, 5),
	}
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.ChannelsGetMessagesRequest)
		if !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		var msgs []tg.MessageClass
		for _, raw := range req.ID {
			if m := store[raw.(*tg.InputMessageID).ID]; m != nil {
				msgs = append(msgs, m)
			}
		}
		return linkedReply(&tg.MessagesChannelMessages{Messages: msgs}, out)
	}))
	b := &telegramLinkBackend{api: api, opts: LinkOptions{}}
	anchor := store[10].(*tg.Message)
	msgs, err := b.series(t.Context(), &tg.InputPeerChannel{ChannelID: 60}, []resourceMessage{{Message: anchor}}, seriesScope(anchor, 0))
	require.NoError(t, err)
	require.Equal(t, []int{10, 12}, seriesIDs(msgs), "a message from the same sender in another topic is skipped")

	reply := &tg.Message{ID: 1}
	reply.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: 3, ReplyToTopID: 2})
	require.True(t, threadMember(reply, 2))
	require.False(t, threadMember(reply, 3))
	require.Equal(t, seriesCaption("Pack A (2/3) https://t.me/x/1"), seriesCaption("pack a 3/3"))
}

func TestBotStartLinkToGroupIsSkippedWithoutHidingGroupMessages(t *testing.T) {
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			if strings.EqualFold(req.Username, "resource_group") {
				return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 60}, Chats: []tg.ChatClass{&tg.Channel{ID: 60, AccessHash: 600, Megagroup: true, Username: "resource_group", Photo: &tg.ChatPhotoEmpty{}}}}, out)
			}
			return tgerr.New(400, "USERNAME_NOT_OCCUPIED")
		case *tg.ChannelsGetMessagesRequest:
			var msgs []tg.MessageClass
			for _, raw := range req.ID {
				if raw.(*tg.InputMessageID).ID == 10 {
					m := linkedDocument(10, 77, "ref", []byte("data"))
					m.PeerID = &tg.PeerChannel{ChannelID: 60}
					msgs = append(msgs, m)
				}
			}
			return linkedReply(&tg.MessagesChannelMessages{Messages: msgs}, out)
		default:
			return fmt.Errorf("unexpected RPC %T: a group must not be started as a bot", in)
		}
	}))
	opts := LinkOptions{}
	require.NoError(t, opts.Normalize())
	backend := &telegramLinkBackend{api: api, manager: peers.Options{}.Build(api), opts: opts}
	cache := &UnavailableLinks{}
	r := &linkResolver{backend: backend, opts: opts, unavailable: cache}
	start, err := parseResourceLink("https://t.me/resource_group?start=abc")
	require.NoError(t, err)
	message, err := parseResourceLink("https://t.me/resource_group/10")
	require.NoError(t, err)
	files, hops, err := r.Resolve(t.Context(), []resourceLink{start, message})
	require.NoError(t, err)
	require.Len(t, files, 1, "the group's message link still resolves after its start link was skipped")
	require.Contains(t, hops[0].Skipped, "points to group resource_group (ID 60), not a bot")
	require.NoError(t, cache.lookup(message))

	cache.remember(resourceLink{Kind: linkKindBot, Chat: "shared"}, tgerr.New(400, "BOT_INVALID"))
	require.Error(t, cache.lookup(resourceLink{Kind: linkKindBot, Chat: "Shared", Start: "other"}))
	require.NoError(t, cache.lookup(resourceLink{Kind: linkKindMessage, Chat: "shared", ID: 3}), "BOT_INVALID only rules out bot requests")
}

func TestBotTextOnlyReplyEndsAfterTextIdle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		timeout    bool
	}{
		{name: "welcome text", text: "欢迎使用！请先关注我们的频道"},
		{name: "progress notice waits for the timeout", text: "正在处理，请稍候", timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := LinkOptions{BotTimeout: 3, BotIdle: 1, BotTextIdle: 1, PollInterval: 10}
			require.NoError(t, opts.Normalize())
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetHistoryRequest:
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesStartBotRequest:
					return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 100}, Message: tc.text}}}}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			backend := &telegramLinkBackend{api: api, opts: opts}
			started := time.Now()
			_, err := backend.requestBotOnce(t.Context(), (&peers.Manager{}).User(&tg.User{ID: 100, Bot: true}), "fixture")
			var dead *botNoResourceError
			require.ErrorAs(t, err, &dead, "both outcomes are dead ends for the resolver")
			require.ErrorContains(t, err, tc.text)
			if tc.timeout {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.GreaterOrEqual(t, time.Since(started), 3*time.Second)
				return
			}
			require.ErrorContains(t, err, "replied without files or resource links")
			require.Less(t, time.Since(started), 2500*time.Millisecond)
		})
	}
}
