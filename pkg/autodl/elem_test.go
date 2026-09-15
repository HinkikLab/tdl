package autodl

import (
	"bytes"
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

	require.NoError(t, e.start(false))
	require.NotNil(t, e.to)

	// simulate two finished parts
	_, err := e.To().WriteAt(bytes.Repeat([]byte{1}, downloader.MaxPartSize), 0)
	require.NoError(t, err)
	e.store.PartDone(0)
	_, err = e.To().WriteAt(bytes.Repeat([]byte{2}, downloader.MaxPartSize), downloader.MaxPartSize)
	require.NoError(t, err)
	e.store.PartDone(1)
	require.NoError(t, e.store.Flush())

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
	require.NoError(t, first.start(false))
	data := bytes.Repeat([]byte{0xAD}, 2*downloader.MaxPartSize)
	_, err := first.To().WriteAt(data, 0)
	require.NoError(t, err)
	first.store.PartDone(0)
	first.store.PartDone(1)
	require.NoError(t, first.closeFile())

	// A restart must preserve the bytes, not just the journal and file size.
	second := newTestElem(path, size)
	require.NoError(t, second.start(false))

	stat, err := second.to.Stat()
	require.NoError(t, err)
	assert.Equal(t, size, stat.Size())

	store := downloader.NewPartsStore(path+tempExt, size)
	assert.Len(t, store.Done(), 2)
	got := make([]byte, len(data))
	_, err = second.to.ReadAt(got, 0)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	require.NoError(t, second.closeFile())
}

func TestElemStaleTempFileIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1_2_video.mp4")

	// a leftover temp file without a sidecar must not be treated as progress
	require.NoError(t, os.WriteFile(path+tempExt, []byte("stale"), 0o644))

	e := newTestElem(path, 5*downloader.MaxPartSize)
	require.NoError(t, e.start(false))

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
	require.NoError(t, e.start(false))

	e.cleanupFile()

	assert.NoFileExists(t, path+tempExt)
	assert.NoFileExists(t, downloader.PartsPath(path+tempExt))
}

func TestElemFinishRenamesAndClearsParts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "1_2_video.mp4")

	e := newTestElem(path, downloader.MaxPartSize)
	require.NoError(t, e.start(false))

	require.NoError(t, e.finish())
	assert.FileExists(t, path)
	assert.NoFileExists(t, downloader.PartsPath(path+tempExt))
}
