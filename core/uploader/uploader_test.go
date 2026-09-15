package uploader

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

type testUploadFile struct{ *bytes.Reader }

func newTestUploadFile() *testUploadFile {
	return &testUploadFile{Reader: bytes.NewReader([]byte("data"))}
}
func (f *testUploadFile) Name() string { return "test.bin" }
func (f *testUploadFile) Size() int64  { return int64(f.Len()) }

type testUploadElem struct{ file *testUploadFile }

func newTestUploadElem() *testUploadElem { return &testUploadElem{file: newTestUploadFile()} }
func (e *testUploadElem) File() File     { return e.file }
func (e *testUploadElem) Thumb() (File, bool) {
	return nil, false
}
func (e *testUploadElem) Caption() (string, []tg.MessageEntityClass) { return "", nil }
func (e *testUploadElem) To() tg.InputPeerClass                      { return &tg.InputPeerSelf{} }
func (e *testUploadElem) Thread() int                                { return 0 }
func (e *testUploadElem) AsPhoto() bool                              { return false }

type testUploadIter struct {
	elems   []Elem
	current Elem
	index   int
	err     error
}

func (i *testUploadIter) Next(context.Context) bool {
	if i.index >= len(i.elems) {
		return false
	}
	i.current = i.elems[i.index]
	i.index++
	return true
}
func (i *testUploadIter) Value() Elem { return i.current }
func (i *testUploadIter) Err() error  { return i.err }

type testUploadProgress struct {
	mu   sync.Mutex
	done []error
}

func (*testUploadProgress) OnAdd(Elem)                   {}
func (*testUploadProgress) OnUpload(Elem, ProgressState) {}
func (p *testUploadProgress) OnDone(_ Elem, err error) {
	p.mu.Lock()
	p.done = append(p.done, err)
	p.mu.Unlock()
}

func TestUploadRejectsInvalidOptions(t *testing.T) {
	ctx := context.Background()
	require.Error(t, New(Options{}).Upload(ctx, 0))
	require.Error(t, New(Options{}).Upload(ctx, 1))
	require.Error(t, New(Options{Client: new(tg.Client)}).Upload(ctx, 1))
	require.Error(t, New(Options{Client: new(tg.Client), Iter: &testUploadIter{}}).Upload(ctx, 1))
}

func TestUploadReportsElementFailure(t *testing.T) {
	want := errors.New("upload failed")
	progress := &testUploadProgress{}
	u := New(Options{
		Client:   new(tg.Client),
		Iter:     &testUploadIter{elems: []Elem{newTestUploadElem()}},
		Progress: progress,
	})
	u.uploadFn = func(context.Context, Elem) error { return want }

	require.ErrorIs(t, u.Upload(context.Background(), 1), want)
	require.Len(t, progress.done, 1)
	require.ErrorIs(t, progress.done[0], want)
}

func TestUploadWaitsForWorkersOnIteratorError(t *testing.T) {
	want := errors.New("iterator failed")
	stopped := make(chan struct{})
	u := New(Options{
		Client: new(tg.Client),
		Iter: &testUploadIter{
			elems: []Elem{newTestUploadElem()},
			err:   want,
		},
		Progress: &testUploadProgress{},
	})
	u.uploadFn = func(ctx context.Context, _ Elem) error {
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}

	err := u.Upload(context.Background(), 1)
	require.ErrorIs(t, err, want)
	select {
	case <-stopped:
	default:
		t.Fatal("upload returned before its worker stopped")
	}
}
