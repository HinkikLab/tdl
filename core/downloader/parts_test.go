package downloader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPartsStoreRejectsInvalidAndMissingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	require.NoError(t, os.WriteFile(path, make([]byte, MaxPartSize), 0600))
	body, err := json.Marshal(partsFile{Version: 1, Parts: 3, Size: 3 * MaxPartSize, Done: []int{-1, 0, 1, 3, 999}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(PartsPath(path), body, 0600))
	require.Equal(t, map[int]struct{}{0: {}}, NewPartsStore(path, 3*MaxPartSize).Done())
	require.NoError(t, os.Remove(path))
	require.Empty(t, NewPartsStore(path, 3*MaxPartSize).Done())
}

func TestPartsStoreRejectsLegacyJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0600))
	require.NoError(t, os.WriteFile(PartsPath(path), []byte(`{"parts":1,"size":3,"done":[0]}`), 0600))
	f, store, err := OpenPartial(path, 3)
	require.NoError(t, err)
	defer f.Close()
	require.Empty(t, store.Done())
	stat, err := f.Stat()
	require.NoError(t, err)
	require.Zero(t, stat.Size())
}

func TestPartsStoreCheckpointAndFinalFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	f, store, err := OpenPartial(path, 8*MaxPartSize)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, f.Truncate(8*MaxPartSize))
	store.PartDone(0)
	first, err := os.ReadFile(PartsPath(path))
	require.NoError(t, err)
	store.PartDone(1)
	second, err := os.ReadFile(PartsPath(path))
	require.NoError(t, err)
	require.Equal(t, first, second, "do not rewrite the journal for every MiB")
	require.NoError(t, store.Flush())
	require.Len(t, NewPartsStore(path, 8*MaxPartSize).Done(), 2)
	store.Reset()
	require.NoError(t, store.Flush())
	require.NoFileExists(t, PartsPath(path))
}

func TestPartsStoreConcurrentCheckpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	f, s, err := OpenPartial(path, 64*MaxPartSize)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, f.Truncate(64*MaxPartSize))
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.PartDone(i) }()
	}
	wg.Wait()
	require.NoError(t, s.Flush())
	require.Len(t, NewPartsStore(path, 64*MaxPartSize).Done(), 64)
}

func BenchmarkPartsCheckpoints(b *testing.B) {
	path := filepath.Join(b.TempDir(), "parts.tmp")
	for i := 0; i < b.N; i++ {
		s := NewPartsStore(path, 128*MaxPartSize)
		s.Reset()
		for part := 0; part < 128; part++ {
			s.PartDone(part)
		}
		if err := s.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
