package downloader

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"
)

type rpcFunc func(context.Context, bin.Encoder, bin.Decoder) error

func (f rpcFunc) Invoke(c context.Context, in bin.Encoder, out bin.Decoder) error {
	return f(c, in, out)
}

type testPool struct{ api *tg.Client }

func (p testPool) Client(context.Context, int) *tg.Client  { return p.api }
func (p testPool) Takeout(context.Context, int) *tg.Client { return p.api }
func (p testPool) Default(context.Context) *tg.Client      { return p.api }
func (p testPool) Close() error                            { return nil }

type callbackProgress struct {
	add      func(Elem)
	done     func(Elem, error)
	download func(ProgressState)
	part     func(int)
}

func (p callbackProgress) OnAdd(e Elem) {
	if p.add != nil {
		p.add(e)
	}
}
func (p callbackProgress) OnDone(e Elem, err error) {
	if p.done != nil {
		p.done(e, err)
	}
}
func (p callbackProgress) OnDownload(e Elem, s ProgressState) {
	if p.download != nil {
		p.download(s)
	}
}
func (p callbackProgress) Resume(Elem) (map[int]struct{}, int64, bool) { return nil, 0, false }
func (p callbackProgress) PartDone(_ Elem, i int) {
	if p.part != nil {
		p.part(i)
	}
}
func (p callbackProgress) Reset(Elem) {}

type singleIter struct {
	elem  Elem
	count int
	next  func(context.Context) bool
	err   error
}

func (i *singleIter) Next(ctx context.Context) bool {
	i.count++
	if i.count == 1 {
		return true
	}
	if i.next != nil {
		return i.next(ctx)
	}
	return false
}
func (i *singleIter) Value() Elem { return i.elem }
func (i *singleIter) Err() error  { return i.err }

func TestDownloadReportsFailureToProgress(t *testing.T) {
	expected := errors.New("fetch failed")
	api := tg.NewClient(rpcFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return expected }))
	el := &failingElem{loc: &tg.InputDocumentFileLocation{}, size: MaxPartSize}
	var got error
	p := callbackProgress{done: func(_ Elem, err error) { got = err }}
	d := New(Options{Pool: testPool{api}, Threads: 1, Iter: &singleIter{elem: el}, Progress: p})
	require.ErrorIs(t, d.Download(context.Background(), 1), expected)
	require.ErrorIs(t, got, expected)
}

func TestDownloadWaitsForWorkersOnIteratorError(t *testing.T) {
	started := make(chan struct{})
	settled := make(chan struct{})
	expected := errors.New("resolve failed")
	api := tg.NewClient(rpcFunc(func(ctx context.Context, _ bin.Encoder, _ bin.Decoder) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	it := &singleIter{elem: &failingElem{loc: &tg.InputDocumentFileLocation{}, size: MaxPartSize}}
	it.next = func(ctx context.Context) bool {
		select {
		case <-started:
			it.err = expected
		case <-ctx.Done():
			it.err = ctx.Err()
		}
		return false
	}
	p := callbackProgress{done: func(_ Elem, err error) { close(settled) }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := New(Options{Pool: testPool{api}, Threads: 1, Iter: it, Progress: p}).Download(ctx, 1)
	require.ErrorIs(t, err, expected)
	select {
	case <-settled:
	default:
		t.Fatal("returned before worker cleanup")
	}
}

func TestDownloadRejectsInvalidLimit(t *testing.T) {
	require.Error(t, New(Options{}).Download(context.Background(), 0))
	require.Error(t, New(Options{}).Download(context.Background(), -1))
}

func TestDownloadRejectsMissingDependencies(t *testing.T) {
	ctx := context.Background()
	require.Error(t, New(Options{Iter: &singleIter{}, Progress: callbackProgress{}}).Download(ctx, 1))
	require.Error(t, New(Options{Pool: testPool{}, Progress: callbackProgress{}}).Download(ctx, 1))
	require.Error(t, New(Options{Pool: testPool{}, Iter: &singleIter{}}).Download(ctx, 1))
}

func TestResumeAlignedTailAndProgress(t *testing.T) {
	size := int64(2*MaxPartSize + 13)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteAt(data[MaxPartSize:2*MaxPartSize], MaxPartSize)
	require.NoError(t, err)
	rpc := &mockRPC{data: data}
	api := tg.NewClient(rpcFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		req := in.(*tg.UploadGetFileRequest)
		if req.Limit != MaxPartSize {
			return errors.New("unaligned Telegram limit")
		}
		return rpc.Invoke(ctx, in, out)
	}))
	var mu sync.Mutex
	var states []ProgressState
	var parts []int
	p := callbackProgress{download: func(s ProgressState) { mu.Lock(); defer mu.Unlock(); states = append(states, s) }, part: func(i int) { mu.Lock(); defer mu.Unlock(); parts = append(parts, i) }}
	el := &testElem{f: f, size: size, loc: &tg.InputDocumentFileLocation{}}
	d := New(Options{Progress: p})
	require.NoError(t, d.parallelIgnore(context.Background(), api, el, map[int]struct{}{1: {}}, 8))
	require.ElementsMatch(t, []int64{0, 2 * MaxPartSize}, rpc.requested())
	require.ElementsMatch(t, []int{0, 2}, parts)
	require.Equal(t, int64(MaxPartSize), states[0].Downloaded)
	require.Equal(t, size, states[len(states)-1].Downloaded)
	require.Equal(t, int64(MaxPartSize), states[len(states)-1].Resumed)
	got, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	require.Equal(t, data, got)
}

func TestResumeRejectsShortResponse(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	defer f.Close()
	marked := false
	d := New(Options{Progress: callbackProgress{part: func(int) { marked = true }}})
	el := &testElem{f: f, size: MaxPartSize, loc: &tg.InputDocumentFileLocation{}}
	require.ErrorIs(t, d.parallelIgnore(context.Background(), tg.NewClient(&mockRPC{data: []byte("short")}), el, nil, 1), io.ErrUnexpectedEOF)
	require.False(t, marked)
}

func TestResumeFailureCancelsOtherWorkers(t *testing.T) {
	blocked := make(chan struct{})
	expected := errors.New("server failure")
	api := tg.NewClient(rpcFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		if in.(*tg.UploadGetFileRequest).Offset == 0 {
			<-blocked
			return expected
		}
		close(blocked)
		<-ctx.Done()
		return ctx.Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	el := &failingElem{loc: &tg.InputDocumentFileLocation{}, size: 2 * MaxPartSize}
	err := New(Options{}).parallelIgnore(ctx, api, el, nil, 2)
	require.ErrorIs(t, err, expected)
	require.NoError(t, ctx.Err(), "must cancel sibling immediately, not wait for the outer deadline")
}

func TestChunkFloodWaitRetries(t *testing.T) {
	calls := 0
	api := tg.NewClient(rpcFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		if calls == 1 {
			return tgerr.New(420, "FLOOD_WAIT_0")
		}
		return (&mockRPC{data: []byte("ok")}).Invoke(ctx, in, out)
	}))
	s := ignoreSchema{sch: api, loc: &tg.InputDocumentFileLocation{}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := s.chunk(ctx, 0, MaxPartSize)
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), got)
	require.Equal(t, 2, calls)
}

func TestChunkFloodWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := tg.NewClient(rpcFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		cancel()
		return tgerr.New(420, "FLOOD_WAIT_60")
	}))
	s := ignoreSchema{sch: api, loc: &tg.InputDocumentFileLocation{}}
	_, err := s.chunk(ctx, 0, MaxPartSize)
	require.ErrorIs(t, err, context.Canceled)
}

type shortWriterElem struct{ failingElem }

func (e *shortWriterElem) To() io.WriterAt                        { return e }
func (e *shortWriterElem) WriteAt(p []byte, _ int64) (int, error) { return len(p) - 1, nil }
func TestWriteAtRejectsShortWrite(t *testing.T) {
	marked := false
	p := callbackProgress{part: func(int) { marked = true }}
	el := &shortWriterElem{failingElem{size: MaxPartSize}}
	n, err := newWriteAt(el, p, p, MaxPartSize).WriteAt([]byte("short"), 0)
	require.Equal(t, 4, n)
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.False(t, marked)
}

func BenchmarkSmallFileWrite(b *testing.B) {
	f, err := os.CreateTemp(b.TempDir(), "small")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	el := &testElem{f: f, size: 1024}
	data := make([]byte, 1024)
	p := callbackProgress{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := newWriteAt(el, p, nil, MaxPartSize).WriteAt(data, 0); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDownloadRejectsIncompleteFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	defer f.Close()
	el := &testElem{f: f, size: MaxPartSize, loc: &tg.InputDocumentFileLocation{}}
	var result error
	var marked bool
	p := callbackProgress{done: func(_ Elem, err error) { result = err }, part: func(int) { marked = true }}
	d := New(Options{Pool: testPool{tg.NewClient(&mockRPC{data: []byte("short")})}, Threads: 1, Iter: &singleIter{elem: el}, Progress: p})
	require.ErrorContains(t, d.Download(context.Background(), 1), "incomplete download")
	require.ErrorContains(t, result, "incomplete download")
	require.False(t, marked, "a truncated response cannot become a completed resume part")
}
