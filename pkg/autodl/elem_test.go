package autodl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/iyear/tdl/core/downloader"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testFile is a downloader.File with a controllable size.
type testFile struct{ size int64 }

func (f testFile) Location() tg.InputFileLocationClass { return &tg.InputDocumentFileLocation{} }
func (f testFile) Size() int64                         { return f.size }
func (f testFile) DC() int                             { return 2 }

func newTestElem(name string, size int64) *elem {
	return newElem(nil, 2, testFile{size: size}, 0, name)
}

func TestElemLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1_2_video.mp4")

	e := newTestElem(path, 3*downloader.MaxPartSize)

	require.NoError(t, e.start(nil, false))
	require.NotNil(t, e.to)

	// simulate two finished parts
	_, err := e.writer.WriteAt([]byte("hello"), 0)
	require.NoError(t, err)
	_, err = e.writer.WriteAt([]byte("world"), downloader.MaxPartSize)
	require.NoError(t, err)

	sidecar := downloader.PartsPath(path + tempExt)
	require.FileExists(t, sidecar)

	// the next run must find those parts again
	store := downloader.NewPartsStore(path+tempExt, e.file.Size())
	done := store.Done()
	assert.Len(t, done, 2)
	assert.Contains(t, done, 0)
	assert.Contains(t, done, 1)

	require.NoError(t, e.finish())
	assert.FileExists(t, path)
	assert.NoFileExists(t, sidecar)
	assert.NoFileExists(t, path+tempExt)
}

func TestElemResumePreAllocates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1_2_big.bin")
	size := int64(5 * downloader.MaxPartSize)

	// first run: write two parts and abandon the temp file
	first := newTestElem(path, size)
	require.NoError(t, first.start(nil, false))
	_, err := first.writer.WriteAt(make([]byte, 8), 0)
	require.NoError(t, err)
	_, err = first.writer.WriteAt(make([]byte, 8), downloader.MaxPartSize)
	require.NoError(t, err)
	require.NoError(t, first.closeFile())

	// second run: the temp file is recreated (and truncated) by start, but the
	// recorded parts must survive and the file must be pre-allocated again
	second := newTestElem(path, size)
	require.NoError(t, second.start(nil, false))

	stat, err := second.to.Stat()
	require.NoError(t, err)
	assert.Equal(t, size, stat.Size())

	store := downloader.NewPartsStore(path+tempExt, size)
	assert.Len(t, store.Done(), 2)

	require.NoError(t, second.closeFile())
}

func TestElemStaleTempFileIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1_2_video.mp4")

	// a leftover temp file without a sidecar must not be treated as progress
	require.NoError(t, os.WriteFile(path+tempExt, []byte("stale"), 0o644))

	e := newTestElem(path, 5*downloader.MaxPartSize)
	require.NoError(t, e.start(nil, false))

	stat, err := e.to.Stat()
	require.NoError(t, err)
	assert.Zero(t, stat.Size())

	assert.Empty(t, downloader.NewPartsStore(path+tempExt, e.file.Size()).Done())

	require.NoError(t, e.closeFile())
}

func TestElemCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1_2_video.mp4")

	e := newTestElem(path, downloader.MaxPartSize)
	require.NoError(t, e.start(nil, false))

	e.cleanupFile()

	assert.NoFileExists(t, path+tempExt)
	assert.NoFileExists(t, downloader.PartsPath(path+tempExt))
}

func TestElemFinishRenamesAndClearsParts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "1_2_video.mp4")

	e := newTestElem(path, downloader.MaxPartSize)
	require.NoError(t, e.start(nil, false))

	require.NoError(t, e.finish())
	assert.FileExists(t, path)
	assert.NoFileExists(t, downloader.PartsPath(path+tempExt))
}

func TestIsComplete(t *testing.T) {
	dir := t.TempDir()

	assert.False(t, isComplete(filepath.Join(dir, "missing")))

	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))
	assert.False(t, isComplete(empty))

	full := filepath.Join(dir, "full")
	require.NoError(t, os.WriteFile(full, []byte("x"), 0o644))
	assert.True(t, isComplete(full))
}
