package chat

import (
	"context"
	"fmt"
	"sync"

	"github.com/gotd/td/tg"
)

// BotUpdates retains only messages from bots currently requested by the batch.
// Deleted messages remain available until their files have been downloaded.
// It implements telegram.UpdateHandler without RPCs on the update loop.
type BotUpdates struct {
	mu      sync.Mutex
	watches map[int64]*botWatch
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
		return result, fmt.Errorf("bot live updates exceed max_bot_messages")
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
