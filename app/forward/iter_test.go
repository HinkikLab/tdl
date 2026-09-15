package forward

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/pkg/tmessage"
)

func TestIterDelayHonorsCancellation(t *testing.T) {
	it := newIter(iterOptions{
		dialogs: []*tmessage.Dialog{{Messages: []int{1, 2}}},
		delay:   time.Hour,
	})
	it.j = 1
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- it.Next(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case ok := <-done:
		require.False(t, ok)
		require.ErrorIs(t, it.err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("iterator ignored cancellation while waiting for delay")
	}
}

func TestNewIterDropsEmptyDialogs(t *testing.T) {
	valid := &tmessage.Dialog{Messages: []int{42}}
	it := newIter(iterOptions{
		dialogs: []*tmessage.Dialog{nil, {}, valid},
	})

	require.Equal(t, []*tmessage.Dialog{valid}, it.opts.dialogs)
}
