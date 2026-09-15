package autodl

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestLegacyStateBindsScopeOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tdl_state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"finished":[1]}`), 0o600))

	_, _, err := LoadStateStore(path, "v1|first")
	require.NoError(t, err)
	_, _, err = LoadStateStore(path, "v1|second")
	assert.Error(t, err)
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
