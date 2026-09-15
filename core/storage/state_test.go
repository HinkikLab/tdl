package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/updates"
	"github.com/stretchr/testify/require"
)

type stateTestStorage struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *stateTestStorage) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	value, ok := s.data[key]
	value = append([]byte(nil), value...)
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	// Widen the read/modify/write window so the regression is deterministic.
	time.Sleep(time.Millisecond)
	return value, nil
}

func (s *stateTestStorage) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	s.data[key] = append([]byte(nil), value...)
	s.mu.Unlock()
	return nil
}

func (s *stateTestStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
	return nil
}

func TestStateKeepsConcurrentChannelUpdates(t *testing.T) {
	kv := &stateTestStorage{data: make(map[string][]byte)}
	state := NewState(kv)
	ctx := context.Background()
	const userID int64 = 1
	require.NoError(t, state.SetState(ctx, userID, updates.State{}))

	const count = 50
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for channelID := 1; channelID <= count; channelID++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- state.SetChannelPts(ctx, userID, int64(channelID), channelID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	found := make(map[int64]int)
	require.NoError(t, state.ForEachChannels(ctx, userID, func(_ context.Context, channelID int64, pts int) error {
		found[channelID] = pts
		return nil
	}))
	require.Len(t, found, count)
}
