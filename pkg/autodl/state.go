package autodl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-faster/errors"
)

// State is the persisted progress of one job.
//
// It backs both resume flavours the python script had: "finished" replaces
// skipped_msg_ids.json (message level resume) and "last_ts" replaces
// tdl_state.json (incremental timestamps).
//
// All methods are safe for concurrent use: the download workers and the
// iterator both record ids while a job runs.
type State struct {
	// Finished holds the message ids that are already downloaded or known to
	// be unavailable, so they are not requested again.
	Finished []int `json:"finished"`
	// Skipped holds the subset of Finished that has no downloadable media,
	// for example deleted messages.
	Skipped []int `json:"skipped"`
	// LastTS is the newest export timestamp that has been fully processed.
	LastTS int64 `json:"last_ts"`

	mu       sync.Mutex
	finished map[int]struct{}
	skipped  map[int]struct{}
}

// NewState returns an empty state.
func NewState() *State {
	return &State{
		finished: make(map[int]struct{}),
		skipped:  make(map[int]struct{}),
	}
}

// LoadState reads a state file. A missing file yields an empty state.
func LoadState(path string) (*State, error) {
	s := NewState()
	if path == "" {
		return s, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, errors.Wrapf(err, "read state %s", path)
	}

	var raw struct {
		Finished []int `json:"finished"`
		Skipped  []int `json:"skipped"`
		LastTS   int64 `json:"last_ts"`
	}
	if err = json.Unmarshal(b, &raw); err != nil {
		return nil, errors.Wrapf(err, "parse state %s", path)
	}

	s.LastTS = raw.LastTS
	for _, id := range raw.Finished {
		s.finished[id] = struct{}{}
	}
	for _, id := range raw.Skipped {
		s.skipped[id] = struct{}{}
		s.finished[id] = struct{}{}
	}

	return s, nil
}

// Save writes the state atomically.
func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}

	s.mu.Lock()
	raw := struct {
		Finished []int `json:"finished"`
		Skipped  []int `json:"skipped"`
		LastTS   int64 `json:"last_ts"`
	}{
		Finished: s.finishedIDs(),
		Skipped:  s.skippedIDs(),
		LastTS:   s.LastTS,
	}
	s.mu.Unlock()

	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return errors.Wrap(err, "marshal state")
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return errors.Wrapf(err, "create state dir %s", dir)
		}
	}

	tmp := path + ".new"
	if err = os.WriteFile(tmp, b, 0o644); err != nil {
		return errors.Wrapf(err, "write state %s", path)
	}

	return os.Rename(tmp, path)
}

// IsFinished reports whether the id is downloaded or skipped.
func (s *State) IsFinished(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.finished[id]
	return ok
}

// IsSkipped reports whether the id is known to have no media.
func (s *State) IsSkipped(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.skipped[id]
	return ok
}

// Finish records ids as done.
func (s *State) Finish(ids ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		s.finished[id] = struct{}{}
	}
}

// Skip records ids as unavailable.
func (s *State) Skip(ids ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		s.finished[id] = struct{}{}
		s.skipped[id] = struct{}{}
	}
}

// ClearSkipped forgets every id that was recorded as unavailable and returns
// how many were forgotten.
//
// It exists because "unavailable" is a guess: a range that was resolved against
// the wrong dialog reports every id as deleted. Retrying them must be possible
// without deleting the whole state file, which would also lose the finished
// messages and the incremental timestamp.
func (s *State) ClearSkipped() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.skipped)
	for id := range s.skipped {
		delete(s.skipped, id)
		delete(s.finished, id)
	}

	return n
}

// SetLastTS updates the incremental timestamp.
func (s *State) SetLastTS(ts int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.LastTS = ts
}

// GetLastTS returns the incremental timestamp.
func (s *State) GetLastTS() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.LastTS
}

// Len returns the number of finished ids.
func (s *State) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.finished)
}

// Missing returns the ids of targets that are neither finished nor skipped.
func (s *State) Missing(targets []int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]int, 0)
	for _, id := range targets {
		if _, ok := s.finished[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// finishedIDs returns a sorted copy. The caller must hold the lock.
func (s *State) finishedIDs() []int {
	out := make([]int, 0, len(s.finished))
	for id := range s.finished {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// skippedIDs returns a sorted copy. The caller must hold the lock.
func (s *State) skippedIDs() []int {
	out := make([]int, 0, len(s.skipped))
	for id := range s.skipped {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// stateStore serializes access to one state file.
//
// Saves are throttled: a job records hundreds of messages and rewriting the
// whole document for each of them would be wasteful. The throttle only delays
// persistence, it never drops it, because the caller always saves once more
// when the job is done.
type stateStore struct {
	path string

	mu       sync.Mutex
	state    *State
	lastSave time.Time
}

// saveInterval is the minimum delay between two automatic state writes.
const saveInterval = 500 * time.Millisecond

// LoadStateStore opens (or creates) the state store of a job.
func LoadStateStore(path string) (*stateStore, *State, error) {
	st, err := LoadState(path)
	if err != nil {
		return nil, nil, err
	}

	return &stateStore{path: path, state: st}, st, nil
}

// Save persists the current state.
func (s *stateStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.state.Save(s.path)
	if err == nil {
		s.lastSave = time.Now()
	}

	return err
}

// SaveThrottled persists the state at most once per saveInterval.
func (s *stateStore) SaveThrottled() error {
	s.mu.Lock()
	if time.Since(s.lastSave) < saveInterval {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	return s.Save()
}
