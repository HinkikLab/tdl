package downloader

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRPCCounts every upload.getFile offset that was requested.
type mockRPC struct {
	mu      sync.Mutex
	offsets []int64

	data []byte
}

func (m *mockRPC) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	req, ok := input.(*tg.UploadGetFileRequest)
	if !ok {
		return nil
	}

	m.mu.Lock()
	m.offsets = append(m.offsets, req.Offset)
	m.mu.Unlock()

	body := []byte(nil)
	if req.Offset < int64(len(m.data)) {
		end := req.Offset + int64(req.Limit)
		if end > int64(len(m.data)) {
			end = int64(len(m.data))
		}
		body = m.data[req.Offset:end]
	}

	var buf bin.Buffer
	if err := (&tg.UploadFile{
		Bytes: body,
		Type:  &tg.StorageFileUnknown{},
	}).Encode(&buf); err != nil {
		return err
	}

	return output.Decode(&buf)
}

func (m *mockRPC) requested() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]int64(nil), m.offsets...)
}

// testElem is a minimal downloader.Elem bound to a file.
type testElem struct {
	f    *os.File
	size int64
	loc  tg.InputFileLocationClass
}

func (e *testElem) File() File                          { return e }
func (e *testElem) To() io.WriterAt                     { return e.f }
func (e *testElem) AsTakeout() bool                     { return false }
func (e *testElem) Location() tg.InputFileLocationClass { return e.loc }
func (e *testElem) Size() int64                         { return e.size }
func (e *testElem) DC() int                             { return 2 }

func (e *testElem) Name() string { return "test.bin" }

func TestParallelIgnoreSkipsParts(t *testing.T) {
	size := int64(3 * MaxPartSize)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	// pretend part 1 is already on disk
	require.NoError(t, PreAllocate(f, size))
	_, err = f.WriteAt(data[MaxPartSize:2*MaxPartSize], MaxPartSize)
	require.NoError(t, err)

	rpc := &mockRPC{data: data}
	el := &testElem{f: f, size: size, loc: &tg.InputDocumentFileLocation{}}

	d := &Downloader{}
	err = d.parallelIgnore(context.Background(), tg.NewClient(rpc), el, map[int]struct{}{1: {}}, 2)
	require.NoError(t, err)

	// only parts 0 and 2 may be fetched
	got := rpc.requested()
	assert.ElementsMatch(t, []int64{0, 2 * MaxPartSize}, got)

	// the whole file must be correct
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, content)
}

// failingElem reports an error for every write.
type failingElem struct {
	loc  tg.InputFileLocationClass
	size int64
}

func (e *failingElem) File() File                          { return e }
func (e *failingElem) To() io.WriterAt                     { return e }
func (e *failingElem) AsTakeout() bool                     { return false }
func (e *failingElem) Location() tg.InputFileLocationClass { return e.loc }
func (e *failingElem) Size() int64                         { return e.size }
func (e *failingElem) DC() int                             { return 2 }
func (e *failingElem) Name() string                        { return "failing.bin" }

func (e *failingElem) WriteAt([]byte, int64) (int, error) { return 0, io.ErrClosedPipe }

// TestParallelIgnoreWriteFailure ensures a dead destination stops the download
// instead of hanging or panicking.
func TestParallelIgnoreWriteFailure(t *testing.T) {
	data := make([]byte, 4*MaxPartSize)
	rpc := &mockRPC{data: data}

	el := &failingElem{loc: &tg.InputDocumentFileLocation{}, size: int64(len(data))}

	d := &Downloader{}

	done := make(chan error, 1)
	go func() {
		done <- d.parallelIgnore(context.Background(), tg.NewClient(rpc), el, nil, 4)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, io.ErrClosedPipe)
	case <-time.After(10 * time.Second):
		t.Fatal("parallelIgnore did not return after a write failure")
	}
}

func TestParallelIgnoreNoSkip(t *testing.T) {
	size := int64(2*MaxPartSize + 10)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	rpc := &mockRPC{data: data}
	el := &testElem{f: f, size: size, loc: &tg.InputDocumentFileLocation{}}

	d := &Downloader{}
	d.SetSkipParts(false)
	require.NoError(t, d.parallelIgnore(context.Background(), tg.NewClient(rpc), el, nil, 3))

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, content)
}
