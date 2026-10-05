package autodl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
)

func TestDrainPropagatesUnconsumedFileCloseFailure(t *testing.T) {
	e := newTestElem(filepath.Join(t.TempDir(), "media.bin"), 10)
	require.NoError(t, e.start(false))
	require.NoError(t, e.to.Close())
	i := &iter{elems: make(chan downloader.Elem, 1)}
	i.elems <- e
	require.ErrorIs(t, i.Drain(), os.ErrClosed)
	require.Nil(t, e.to)
	require.NoError(t, i.Drain(), "repeated cleanup is safe")
}

func TestDrainPropagatesUnconsumedJournalFlushFailureAndClosesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.bin")
	e := newTestElem(path, 10)
	require.NoError(t, e.start(false))
	e.store.PartDone(0)
	require.NoError(t, os.Mkdir(downloader.PartsPath(path+tempExt)+".new", 0700))
	i := &iter{elems: make(chan downloader.Elem, 1)}
	i.elems <- e
	require.Error(t, i.Drain())
	require.Nil(t, e.to)
	// Windows proves the unconsumed payload handle has actually been closed.
	require.NoError(t, os.Remove(path+tempExt))
}
