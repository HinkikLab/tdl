package forward

import (
	"sync"
	"testing"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/forwarder"
)

func TestProgressSupportsConcurrentCloneUpdates(t *testing.T) {
	manager := &peers.Manager{}
	elem := &iterElem{
		from: manager.Channel(&tg.Channel{ID: 1, Title: "source"}),
		to:   manager.Channel(&tg.Channel{ID: 2, Title: "target"}),
		msg:  &tg.Message{ID: 3},
	}
	progress := newProgress(pw.NewWriter())
	progress.OnAdd(elem)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			progress.OnClone(elem, forwarder.ProgressState{Done: int64(i), Total: 20})
		}()
	}
	wg.Wait()
	progress.OnDone(elem, nil)

	_, ok := progress.trackers.Load(progress.tuple(elem))
	require.False(t, ok)
}
