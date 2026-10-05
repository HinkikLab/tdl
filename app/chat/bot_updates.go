package chat

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// BotUpdates retains only messages from bots currently requested by the batch.
// Deleted messages remain available until their files have been downloaded.
// It implements telegram.UpdateHandler without RPCs on the update loop.
type BotUpdates struct {
	mu          sync.Mutex
	watches     map[int64]*botWatch
	lastReplies map[int64]botReplyTime
}

type botReplyTime struct {
	messageID int
	sentAt    time.Time
}

func (u *BotUpdates) recordBotReply(botID int64, m *tg.Message) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.recordBotReplyLocked(botID, m)
}

func (u *BotUpdates) recordBotReplyLocked(botID int64, m *tg.Message) {
	last := u.lastReplies[botID]
	if m.Out || m.ID <= last.messageID {
		return
	}
	if u.lastReplies == nil {
		u.lastReplies = map[int64]botReplyTime{}
	}
	last.messageID = m.ID
	sentAt := time.Unix(int64(m.Date), 0)
	if m.Date <= 0 {
		sentAt = time.Now()
	}
	if sentAt.After(last.sentAt) {
		last.sentAt = sentAt
	}
	u.lastReplies[botID] = last
}

func (u *BotUpdates) lastBotReply(botID int64) botReplyTime {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastReplies[botID]
}

type botWatch struct {
	after, limit int
	messages     map[int]*tg.Message
	overflow     bool
}

func (u *BotUpdates) Watch(botID int64, after, limit int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.watches == nil {
		u.watches = map[int64]*botWatch{}
	}
	if u.watches[botID] == nil {
		u.watches[botID] = &botWatch{after: after, limit: limit, messages: map[int]*tg.Message{}}
	}
	if u.lastReplies == nil {
		u.lastReplies = map[int64]botReplyTime{}
	}
	last := u.lastReplies[botID]
	last.messageID = max(last.messageID, after)
	u.lastReplies[botID] = last
}

func (u *BotUpdates) Handle(_ context.Context, updates tg.UpdatesClass) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, m := range updateMessages(updates) {
		peer, ok := m.PeerID.(*tg.PeerUser)
		if !ok {
			continue
		}
		watch := u.watches[peer.UserID]
		if _, known := u.lastReplies[peer.UserID]; watch != nil || known {
			u.recordBotReplyLocked(peer.UserID, m)
		}
		if watch == nil || m.ID <= watch.after {
			continue
		}
		if watch.messages[m.ID] == nil && len(watch.messages) >= watch.limit {
			watch.overflow = true
			continue
		}
		watch.messages[m.ID] = m
	}
	return nil
}

func (u *BotUpdates) Snapshot(botID int64, after int) ([]*tg.Message, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	watch := u.watches[botID]
	if watch == nil {
		return nil, nil
	}
	var result []*tg.Message
	for _, m := range watch.messages {
		if m.ID > after {
			result = append(result, m)
		}
	}
	if watch.overflow {
		return result, diagnostic.Describe(fmt.Errorf("bot live updates exceed max_bot_messages"), corei18n.Message{ID: "errors.message.bot_live_updates_exceed_max_key_bot_key_messages"})
	}
	return result, nil
}

func (u *BotUpdates) Stop(botID int64) []*tg.Message {
	u.mu.Lock()
	defer u.mu.Unlock()
	watch := u.watches[botID]
	if watch == nil {
		return nil
	}
	delete(u.watches, botID)
	var result []*tg.Message
	for _, m := range watch.messages {
		result = append(result, m)
	}
	return result
}
