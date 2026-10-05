package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func partIdentity(size int64) FileIdentity {
	return FileIdentity{Kind: "document", ID: 42, Size: size, DC: 2, PartSize: MaxPartSize}
}

func TestPartsStoreRejectsInvalidAndMissingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	id := partIdentity(3 * MaxPartSize)
	require.NoError(t, os.WriteFile(path, make([]byte, MaxPartSize), 0600))
	body, err := json.Marshal(partsFile{Version: 2, Parts: 3, Size: id.Size, Identity: id, Done: []int{-1, 0, 1, 3, 999}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(PartsPath(path), body, 0600))
	require.Equal(t, map[int]struct{}{0: {}}, NewPartsStore(path, id.Size, id).Done())
	require.NoError(t, os.Remove(path))
	require.Empty(t, NewPartsStore(path, id.Size, id).Done())
	got, err := os.ReadFile(PartsPath(path))
	require.NoError(t, err)
	require.Equal(t, body, got, "loading state must not delete recovery evidence")
}

func TestPartsStorePreservesUnverifiedData(t *testing.T) {
	for _, journal := range []string{
		`{"parts":1,"size":3,"done":[0]}`,
		`{"version":1,"parts":1,"size":3,"done":[0]}`,
		`{"version":2,"parts":1,"size":3,"done":[0]}`,
		`broken`,
	} {
		t.Run(journal, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.tmp")
			require.NoError(t, os.WriteFile(path, []byte("old"), 0600))
			require.NoError(t, os.WriteFile(PartsPath(path), []byte(journal), 0600))
			s := NewPartsStore(path, 3, partIdentity(3))
			require.Empty(t, s.Done())
			require.FileExists(t, path)
			require.FileExists(t, PartsPath(path))
			f, store, err := OpenPartial(path, 3, partIdentity(3))
			require.NoError(t, err)
			defer f.Close()
			require.Empty(t, store.Done())
			require.Equal(t, []string{path + ".unverified", PartsPath(path + ".unverified")}, store.RecoveryPaths())
			data, err := os.ReadFile(store.RecoveryPaths()[0])
			require.NoError(t, err)
			require.Equal(t, []byte("old"), data)
			body, err := os.ReadFile(store.RecoveryPaths()[1])
			require.NoError(t, err)
			require.Equal(t, []byte(journal), body)
			stat, err := f.Stat()
			require.NoError(t, err)
			require.Zero(t, stat.Size())
		})
	}
}

func TestPartsStoreCheckpointAndFinalFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	id := partIdentity(64 * MaxPartSize)
	f, store, err := OpenPartial(path, id.Size, id)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, f.Truncate(id.Size))
	for i := 0; i < 31; i++ {
		store.PartDone(i)
	}
	require.NoFileExists(t, PartsPath(path), "checkpoint after 32 parts, not every MiB")
	store.PartDone(31)
	first, err := os.ReadFile(PartsPath(path))
	require.NoError(t, err)
	require.Len(t, NewPartsStore(path, id.Size, id).Done(), 32)
	store.PartDone(32)
	second, err := os.ReadFile(PartsPath(path))
	require.NoError(t, err)
	require.Equal(t, first, second)
	// Exercise the time threshold without a sleeping or timing-sensitive test.
	store.lastSave = time.Now().Add(-2 * time.Second)
	store.PartDone(33)
	require.Len(t, NewPartsStore(path, id.Size, id).Done(), 34)
	store.PartDone(34)
	store.PartDone(34)
	require.NoError(t, store.Flush())
	require.Len(t, NewPartsStore(path, id.Size, id).Done(), 35)
	store.Reset()
	require.NoError(t, store.Flush())
	require.NoFileExists(t, PartsPath(path))
}

func TestPartsStoreConcurrentCheckpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	id := partIdentity(64 * MaxPartSize)
	f, s, err := OpenPartial(path, id.Size, id)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, f.Truncate(id.Size))
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.PartDone(i) }()
	}
	wg.Wait()
	require.NoError(t, s.Flush())
	require.Len(t, NewPartsStore(path, id.Size, id).Done(), 64)
}

func TestConcurrentFilesHaveIndependentJournals(t *testing.T) {
	dir := t.TempDir()
	var stores []*PartsStore
	var identities []FileIdentity
	var paths []string
	for _, name := range []string{"first.tmp", "second.tmp"} {
		path := filepath.Join(dir, name)
		id := partIdentity(4 * MaxPartSize)
		id.ID += int64(len(stores))
		f, s, err := OpenPartial(path, id.Size, id)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(id.Size))
		require.NoError(t, f.Close())
		paths = append(paths, path)
		identities = append(identities, id)
		stores = append(stores, s)
	}
	var wg sync.WaitGroup
	for n, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.PartDone(n)
			require.NoError(t, s.Flush())
		}()
	}
	wg.Wait()
	for n, path := range paths {
		require.Equal(t, map[int]struct{}{n: {}}, NewPartsStore(path, identities[n].Size, identities[n]).Done())
	}
}

func TestChangedPartSpecificationDoesNotReuseJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	id := partIdentity(3)
	require.NoError(t, os.WriteFile(path, []byte("old"), 0600))
	journal := partsFile{Version: 2, Parts: 1, Size: id.Size, Identity: id, Done: []int{0}}
	journal.Identity.PartSize = MaxPartSize / 2
	body, err := json.Marshal(journal)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(PartsPath(path), body, 0600))
	f, s, err := OpenPartial(path, id.Size, id)
	require.NoError(t, err)
	defer f.Close()
	require.Empty(t, s.Done())
	require.Len(t, s.RecoveryPaths(), 2)
}

func TestPartialIdentityReplacementAndReferenceRenewal(t *testing.T) {
	oldIdentity := partIdentity(2 * MaxPartSize)
	for name, mutate := range map[string]func(*FileIdentity){
		"same file":            func(*FileIdentity) {},
		"new file":             func(id *FileIdentity) { id.ID++ },
		"different photo kind": func(id *FileIdentity) { id.Kind = "photo" },
		"photo size selection": func(id *FileIdentity) { id.ThumbSize = "x" },
		"different DC":         func(id *FileIdentity) { id.DC++ },
		"different byte size":  func(id *FileIdentity) { id.Size++ },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.tmp")
			old := bytes.Repeat([]byte("A"), MaxPartSize)
			f, store, err := OpenPartial(path, oldIdentity.Size, oldIdentity)
			require.NoError(t, err)
			_, err = f.WriteAt(old, 0)
			require.NoError(t, err)
			store.PartDone(0)
			require.NoError(t, store.Flush())
			require.NoError(t, f.Close())
			id := oldIdentity
			mutate(&id)
			f, store, err = OpenPartial(path, id.Size, id)
			require.NoError(t, err)
			defer f.Close()
			if id == oldIdentity {
				require.Equal(t, map[int]struct{}{0: {}}, store.Done())
				require.Empty(t, store.RecoveryPaths())
			} else {
				require.Empty(t, store.Done())
				require.Len(t, store.RecoveryPaths(), 2)
				got, err := os.ReadFile(store.RecoveryPaths()[0])
				require.NoError(t, err)
				require.Equal(t, old, got)
			}
		})
	}
}

func TestSameSizeReplacementCannotMixBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	size := int64(2 * MaxPartSize)
	old := &testElem{size: size, loc: &tg.InputDocumentFileLocation{ID: 42, FileReference: []byte("old")}}
	f, store, err := OpenPartialFile(path, old)
	require.NoError(t, err)
	_, err = f.WriteAt(bytes.Repeat([]byte("A"), MaxPartSize), 0)
	require.NoError(t, err)
	store.PartDone(0)
	require.NoError(t, store.Flush())
	require.NoError(t, f.Close())
	fresh := &testElem{size: size, loc: &tg.InputDocumentFileLocation{ID: 999, FileReference: []byte("new")}}
	fresh.f, store, err = OpenPartialFile(path, fresh)
	require.NoError(t, err)
	defer fresh.f.Close()
	rpc := &mockRPC{data: bytes.Repeat([]byte("B"), int(size))}
	require.NoError(t, New(Options{}).parallelIgnore(context.Background(), tg.NewClient(rpc), fresh, store.Done(), 2))
	require.ElementsMatch(t, []int64{0, MaxPartSize}, rpc.requested())
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, rpc.data, got)
}

func TestRenewedReferenceOnlyDownloadsMissingParts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	data := bytes.Repeat([]byte("B"), 2*MaxPartSize)
	old := &testElem{size: int64(len(data)), loc: &tg.InputDocumentFileLocation{ID: 42, AccessHash: 43, FileReference: []byte("old")}}
	f, store, err := OpenPartialFile(path, old)
	require.NoError(t, err)
	_, err = f.WriteAt(data[:MaxPartSize], 0)
	require.NoError(t, err)
	store.PartDone(0)
	require.NoError(t, store.Flush())
	require.NoError(t, f.Close())
	fresh := &testElem{size: int64(len(data)), loc: &tg.InputDocumentFileLocation{ID: 42, AccessHash: 99, FileReference: []byte("new")}}
	fresh.f, store, err = OpenPartialFile(path, fresh)
	require.NoError(t, err)
	defer fresh.f.Close()
	rpc := &mockRPC{data: data}
	require.NoError(t, New(Options{}).parallelIgnore(context.Background(), tg.NewClient(rpc), fresh, store.Done(), 2))
	require.Equal(t, []int64{MaxPartSize}, rpc.requested())
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, got)
}

func TestPartsStoreRecoveryDoesNotOverwriteEarlierBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	require.NoError(t, os.WriteFile(path+".unverified", []byte("previous"), 0600))
	require.NoError(t, os.WriteFile(path, []byte("current"), 0600))
	f, s, err := OpenPartial(path, 7, partIdentity(7))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Equal(t, []string{path + ".unverified.1"}, s.RecoveryPaths())
	got, err := os.ReadFile(path + ".unverified")
	require.NoError(t, err)
	require.Equal(t, []byte("previous"), got)
}

func TestFileIdentityValidation(t *testing.T) {
	_, err := FileIdentityOf(&testElem{size: 3, loc: &tg.InputDocumentFileLocation{ID: -42}})
	require.NoError(t, err, "Telegram file IDs are signed int64 values")
	for _, file := range []File{nil, &testElem{size: -1, loc: &tg.InputDocumentFileLocation{ID: 42}}, &testElem{size: 3, loc: &tg.InputDocumentFileLocation{}}, &testElem{size: 3, loc: &tg.InputFileLocation{}}} {
		_, err := FileIdentityOf(file)
		require.Error(t, err)
	}
}

func TestPartsStoreFlushFailureRetainsPendingCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	s := NewPartsStore(path, 3, partIdentity(3))
	s.PartDone(0)
	staging := PartsPath(path) + ".new"
	require.NoError(t, os.Mkdir(staging, 0700))
	require.Error(t, s.Flush())
	require.Equal(t, map[int]struct{}{0: {}}, s.Done())
	require.NoError(t, os.Remove(staging))
	require.NoError(t, s.Flush())
	body, err := os.ReadFile(PartsPath(path))
	require.NoError(t, err)
	var journal partsFile
	require.NoError(t, json.Unmarshal(body, &journal))
	require.Equal(t, []int{0}, journal.Done)
}

func TestPartsStoreCleanupFailureRetainsTracking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	s := NewPartsStore(path, 3, partIdentity(3))
	s.PartDone(0)
	require.NoError(t, os.Mkdir(PartsPath(path), 0700))
	child := filepath.Join(PartsPath(path), "occupied")
	require.NoError(t, os.WriteFile(child, nil, 0600))
	require.Error(t, s.RemoveChecked())
	require.Equal(t, map[int]struct{}{0: {}}, s.Done())
	require.NoError(t, os.Remove(child))
	require.NoError(t, s.RemoveChecked())
	require.Empty(t, s.Done())
}

func TestOpenPartialWithoutIdentityDoesNotTrustBoundJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.tmp")
	id := partIdentity(3)
	f, s, err := OpenPartial(path, id.Size, id)
	require.NoError(t, err)
	_, err = f.WriteString("old")
	require.NoError(t, err)
	s.PartDone(0)
	require.NoError(t, s.Flush())
	require.NoError(t, f.Close())
	f, s, err = OpenPartial(path, id.Size)
	require.NoError(t, err)
	defer f.Close()
	require.Empty(t, s.Done())
	require.Len(t, s.RecoveryPaths(), 2)
}

func BenchmarkPartsCheckpoints(b *testing.B) {
	path := filepath.Join(b.TempDir(), "parts.tmp")
	id := partIdentity(128 * MaxPartSize)
	b.ReportMetric(4, "journals/op")
	for i := 0; i < b.N; i++ {
		s := NewPartsStore(path, id.Size, id)
		s.Reset()
		for part := 0; part < 128; part++ {
			s.PartDone(part)
		}
		if err := s.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
