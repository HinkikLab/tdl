package autodl

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tdl_state.json")

	s := NewState()
	s.Finish(1, 2, 3)
	s.Skip(9)
	s.SetLastTS(1700000000)
	require.NoError(t, s.Save(path))

	loaded, err := LoadState(path)
	require.NoError(t, err)

	assert.Equal(t, 4, loaded.Len())
	assert.True(t, loaded.IsFinished(1))
	assert.True(t, loaded.IsFinished(9))
	assert.True(t, loaded.IsSkipped(9))
	assert.False(t, loaded.IsSkipped(1))
	assert.Equal(t, int64(1700000000), loaded.GetLastTS())
	assert.Equal(t, []int{4, 5}, loaded.Missing([]int{1, 4, 5, 9}))
}

func TestLoadStateMissingFile(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "nope.json"))
	require.NoError(t, err)
	assert.Equal(t, 0, s.Len())
}

func TestLoadStateInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	_, err := LoadState(path)
	assert.Error(t, err)
}

// TestStateConcurrentAccess mirrors the real access pattern: the iterator
// records finished ids while the download workers record their own.
func TestStateConcurrentAccess(t *testing.T) {
	s := NewState()

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s.Finish(worker*200 + i)
				s.IsFinished(worker*200 + i)
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, 1600, s.Len())
}

func TestStateSaveCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "tdl_state.json")

	s := NewState()
	s.Finish(1)
	require.NoError(t, s.Save(path))

	_, err := os.Stat(path)
	require.NoError(t, err)
}

// TestStateClearSkipped checks the recovery path for ids that an earlier run
// recorded as unavailable, which is what --retry-skipped uses.
func TestStateClearSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tdl_state.json")

	s := NewState()
	s.Finish(1)
	s.Skip(2, 3)
	s.SetLastTS(1700000000)
	require.NoError(t, s.Save(path))

	loaded, err := LoadState(path)
	require.NoError(t, err)

	assert.Equal(t, 2, loaded.ClearSkipped())
	// the skipped ids are downloadable again, the finished one is untouched
	assert.Equal(t, []int{2, 3}, loaded.Missing([]int{1, 2, 3}))
	assert.False(t, loaded.IsSkipped(2))
	assert.False(t, loaded.IsFinished(2))
	assert.True(t, loaded.IsFinished(1))
	// the incremental timestamp survives, it is not part of the skip cache
	assert.Equal(t, int64(1700000000), loaded.GetLastTS())

	require.NoError(t, loaded.Save(path))

	reloaded, err := LoadState(path)
	require.NoError(t, err)
	assert.Equal(t, 0, reloaded.ClearSkipped())
	assert.Equal(t, []int{2, 3}, reloaded.Missing([]int{1, 2, 3}))
}

func TestStateScopeRejectsAnotherDialog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tdl_state.json")
	store, state, err := LoadStateStore(path, "v1|first")
	require.NoError(t, err)
	state.Finish(1)
	require.NoError(t, store.Save())

	_, _, err = LoadStateStore(path, "v1|second")
	assert.Error(t, err)
}

func TestLegacyPopulatedStateRefusesUnverifiableIdentityWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tdl_state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"finished":[1]}`), 0o600))

	before, err := os.ReadFile(path)
	require.NoError(t, err)
	_, _, err = LoadStateStore(path, "v3|first")
	require.ErrorContains(t, err, "no verifiable source identity")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestStateScopeIncludesOutputSemantics(t *testing.T) {
	job := &Job{mode: ModeDirect}
	link := Link{Chat: "Channel"}
	r := &Runner{opts: Options{Template: "{{.MessageID}}", Include: []string{"MP4", ".jpg"}}}
	base := r.stateScope(job, link, "downloads")

	reordered := &Runner{opts: Options{Template: "{{.MessageID}}", Include: []string{"jpg", ".mp4"}}}
	assert.Equal(t, base, reordered.stateScope(job, link, "downloads"))
	assert.NotEqual(t, base, r.stateScope(job, link, "other"))
	r.opts.Template = "{{.FileName}}"
	assert.NotEqual(t, base, r.stateScope(job, link, "downloads"))
}

func TestLoadEmptyLegacyStateOnlyBindsWhenExplicitlySaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := []byte(`{"finished":[],"last_ts":0}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	store, state, err := LoadStateStore(path, "v3|source")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, data)
	require.Equal(t, "v3|source", state.Scope)
	require.NoError(t, store.Save())
	_, _, err = LoadStateStore(path, "v3|other")
	require.Error(t, err)
}

func TestCleanStateSaveDoesNotRewriteAndCopiesToNewPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := NewState()
	state.Finish(7)
	require.NoError(t, state.Save(path))
	old, err := os.Stat(path)
	require.NoError(t, err)
	state.Finish(7)
	require.NoError(t, state.Save(path))
	now, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, old.ModTime(), now.ModTime())
	copyPath := filepath.Join(t.TempDir(), "copy.json")
	require.NoError(t, state.Save(copyPath))
	require.FileExists(t, copyPath)
	require.NoError(t, os.Remove(copyPath))
	require.NoError(t, state.Save(copyPath))
	require.FileExists(t, copyPath)
}

func TestConcurrentMutationAndSaveDoesNotLoseDirtyChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := NewState()
	var wait sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for id := 0; id < 100; id++ {
				state.Finish(worker*100 + id)
				if id%10 == 0 {
					require.NoError(t, state.Save(path))
				}
			}
		}(worker)
	}
	wait.Wait()
	require.NoError(t, state.Save(path))
	loaded, err := LoadState(path)
	require.NoError(t, err)
	require.Equal(t, 300, loaded.Len())
}

func TestCanceledIncrementalWindowAndFailedSavePreserveTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, state, err := LoadStateStore(path)
	require.NoError(t, err)
	state.SetLastTS(10)
	state.Finish(7)
	require.NoError(t, store.Save())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{}
	for _, targets := range [][]int{nil, {7}} {
		require.ErrorIs(t, r.advanceIncremental(ctx, &Job{}, store, state, targets, time.Now().Unix()), context.Canceled)
		require.Equal(t, int64(10), state.GetLastTS())
	}
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	store.path = filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.Mkdir(store.path, 0o700))
	require.Error(t, r.advanceIncremental(context.Background(), &Job{}, store, state, nil, 20))
	require.Equal(t, int64(10), state.GetLastTS())
}

func TestStateMediaTerminalRecordsRoundTripAndForget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := NewState()
	identity := downloader.FileIdentity{Kind: "document", ID: 42, Size: 100, DC: 2, PartSize: downloader.MaxPartSize}
	output := filepath.Join(t.TempDir(), "media.bin")
	require.NoError(t, os.WriteFile(output, make([]byte, 100), 0o600))
	require.NoError(t, state.CompleteMedia(7, identity, output))
	state.Filter(8)
	state.Skip(9)
	require.NoError(t, state.Save(path))
	loaded, err := LoadState(path)
	require.NoError(t, err)
	record, exists := loaded.Media(7)
	require.True(t, exists)
	require.Equal(t, identity, record.Identity)
	require.Equal(t, canonicalPath(output), record.Path)
	require.False(t, loaded.IsTerminalWithoutMedia(7))
	require.True(t, loaded.IsTerminalWithoutMedia(8))
	require.True(t, loaded.IsTerminalWithoutMedia(9))
	loaded.ForgetMedia(7)
	require.False(t, loaded.IsFinished(7))
	_, exists = loaded.Media(7)
	require.False(t, exists)
	require.NoError(t, loaded.CompleteMedia(8, identity, output))
	require.False(t, loaded.IsTerminalWithoutMedia(8))
	require.NoError(t, loaded.Save(path))
	reloaded, err := LoadState(path)
	require.NoError(t, err)
	require.False(t, reloaded.IsFinished(7))
	require.False(t, reloaded.IsTerminalWithoutMedia(8))
}
