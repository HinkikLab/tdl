package forwarder

import (
	"context"
	"testing"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type testForwardElem struct {
	from peers.Peer
	to   peers.Peer
	msg  *tg.Message
}

func (e *testForwardElem) Mode() Mode       { return ModeClone }
func (e *testForwardElem) From() peers.Peer { return e.from }
func (e *testForwardElem) Msg() *tg.Message { return e.msg }
func (e *testForwardElem) To() peers.Peer   { return e.to }
func (e *testForwardElem) Thread() int      { return 0 }
func (e *testForwardElem) AsSilent() bool   { return false }
func (e *testForwardElem) AsDryRun() bool   { return true }
func (e *testForwardElem) AsGrouped() bool  { return true }

type testForwardProgress struct{}

func (testForwardProgress) OnAdd(Elem)                  {}
func (testForwardProgress) OnClone(Elem, ProgressState) {}
func (testForwardProgress) OnDone(Elem, error)          {}

func TestForwardValidatesDependencies(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		err  string
	}{
		{name: "pool", opts: Options{}, err: "forward pool is required"},
		{name: "iterator", opts: Options{Pool: testForwardPool{}}, err: "forward iterator is required"},
		{name: "progress", opts: Options{Pool: testForwardPool{}, Iter: testForwardIter{}}, err: "forward progress is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualError(t, New(tt.opts).Forward(context.Background()), tt.err)
		})
	}
}

type testForwardPool struct{}

func (testForwardPool) Client(context.Context, int) *tg.Client  { return nil }
func (testForwardPool) Takeout(context.Context, int) *tg.Client { return nil }
func (testForwardPool) Default(context.Context) *tg.Client      { return nil }
func (testForwardPool) Close() error                            { return nil }

type testForwardIter struct{}

func (testForwardIter) Next(context.Context) bool { return false }
func (testForwardIter) Value() Elem               { return nil }
func (testForwardIter) Err() error                { return nil }

type oneForwardIter struct {
	elem Elem
	done bool
}

func (i *oneForwardIter) Next(context.Context) bool {
	if i.done {
		return false
	}
	i.done = true
	return true
}
func (i *oneForwardIter) Value() Elem { return i.elem }
func (i *oneForwardIter) Err() error  { return nil }

func TestForwardReturnsElementFailure(t *testing.T) {
	manager := &peers.Manager{}
	elem := &testForwardElem{
		from: manager.Channel(&tg.Channel{ID: 1}),
		to:   manager.Channel(&tg.Channel{ID: 2}),
		msg:  &tg.Message{ID: 10},
	}
	f := New(Options{
		Pool:     testForwardPool{},
		Iter:     &oneForwardIter{elem: elem},
		Progress: testForwardProgress{},
	})

	require.ErrorContains(t, f.Forward(context.Background()), "empty message content")
}

func TestFailedGroupIsNotMarkedSent(t *testing.T) {
	manager := &peers.Manager{}
	elem := &testForwardElem{
		from: manager.Channel(&tg.Channel{ID: 1}),
		to:   manager.Channel(&tg.Channel{ID: 2}),
		msg:  &tg.Message{ID: 10},
	}
	f := New(Options{Progress: testForwardProgress{}})

	err := f.forwardMessage(context.Background(), elem, &tg.Message{ID: 11})
	require.Error(t, err)
	require.Empty(t, f.sent)
}
