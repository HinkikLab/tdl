package autodl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-faster/errors"

	"github.com/iyear/tdl/core/diagnostic"
	"github.com/iyear/tdl/core/downloader"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// MediaRecord binds a committed message to the exact media and output path.
type MediaRecord struct {
	Identity  downloader.FileIdentity `json:"identity"`
	Path      string                  `json:"path"`
	Size      int64                   `json:"size"`
	ModTimeNS int64                   `json:"mod_time_ns"`
}

func (record MediaRecord) MatchesStat(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && record.ModTimeNS != 0 && record.Size == info.Size() && record.ModTimeNS == info.ModTime().UnixNano()
}

// State is the persisted progress of one job.
//
// It backs both resume flavours the python script had: "finished" replaces
// skipped_msg_ids.json (message level resume) and "last_ts" replaces
// tdl_state.json (incremental timestamps).
//
// All methods are safe for concurrent use: the download workers and the
// iterator both record ids while a job runs.
type State struct {
	// Scope identifies the Telegram dialog and mode this state belongs to.
	// It prevents colliding message IDs when multiple jobs share a state path.
	Scope string `json:"scope,omitempty"`
	// Finished holds the message ids that are already downloaded or known to
	// be unavailable, so they are not requested again.
	Finished []int `json:"finished"`
	// Skipped holds the subset of Finished that has no downloadable media,
	// for example deleted messages.
	Skipped []int `json:"skipped"`
	// LastTS is the newest export timestamp that has been fully processed.
	LastTS       int64               `json:"last_ts"`
	MediaRecords map[int]MediaRecord `json:"media,omitempty"`
	Filtered     []int               `json:"filtered,omitempty"`

	mu       sync.Mutex
	finished map[int]struct{}
	skipped  map[int]struct{}
	media    map[int]MediaRecord
	filtered map[int]struct{}
	// needsScopeSave is set when a legacy unscoped file is claimed by a job.
	needsScopeSave bool
	revision       uint64
	persisted      uint64
	persistedPath  string
	saveMu         sync.Mutex
}

// NewState returns an empty state.
func NewState() *State {
	return &State{
		finished: make(map[int]struct{}),
		skipped:  make(map[int]struct{}),
		media:    make(map[int]MediaRecord),
		filtered: make(map[int]struct{}),
		revision: 1,
	}
}

// LoadState reads a state file. A missing file yields an empty state.
func LoadState(path string) (*State, error) {
	return loadState(path, "")
}

func loadState(path, scope string) (*State, error) {
	s := NewState()
	s.Scope = scope
	if path == "" {
		return s, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, diagnostic.Describe(errors.Wrapf(err, "read state %s", path), corei18n.Message{ID: "errors.context.read_state_value", Args: map[string]any{"Arg1": path, "Reason": err}})
	}

	var raw struct {
		Scope        string              `json:"scope"`
		Finished     []int               `json:"finished"`
		Skipped      []int               `json:"skipped"`
		LastTS       int64               `json:"last_ts"`
		MediaRecords map[int]MediaRecord `json:"media"`
		Filtered     []int               `json:"filtered"`
	}
	if err = json.Unmarshal(b, &raw); err != nil {
		return nil, diagnostic.Describe(errors.Wrapf(err, "parse state %s", path), corei18n.Message{ID: "errors.context.parse_state_value", Args: map[string]any{"Arg1": path, "Reason": err}})
	}
	if scope != "" && raw.Scope != "" && raw.Scope != scope {
		return nil, diagnostic.Describe(errors.Errorf("state %s belongs to %q, not %q; preserved unchanged: use a new state file/subdir to rescan, or keep a backup before explicitly resetting it", path, raw.Scope, scope), corei18n.Message{ID: "errors.batch.state_scope_mismatch", Args: map[string]any{"Arg1": path, "Arg2": fmt.Sprintf("%q", raw.Scope), "Arg3": fmt.Sprintf("%q", scope)}})
	}
	if scope != "" && raw.Scope == "" && (len(raw.Finished) > 0 || len(raw.Skipped) > 0 || len(raw.MediaRecords) > 0 || len(raw.Filtered) > 0 || raw.LastTS > 0) {
		return nil, diagnostic.Describe(errors.Errorf("legacy state %s has no verifiable source identity; preserved unchanged: use a new state file/subdir to rescan, or keep a backup before explicitly resetting it", path), corei18n.Message{ID: "errors.batch.legacy_state_scope_missing", Args: map[string]any{"Arg1": path}})
	}

	s.Scope = raw.Scope
	if s.Scope == "" {
		s.Scope = scope
		s.needsScopeSave = scope != ""
	}
	s.LastTS = raw.LastTS
	for _, id := range raw.Finished {
		s.finished[id] = struct{}{}
	}
	for _, id := range raw.Skipped {
		s.skipped[id] = struct{}{}
		s.finished[id] = struct{}{}
	}
	for id, record := range raw.MediaRecords {
		s.media[id] = record
		s.finished[id] = struct{}{}
	}
	for _, id := range raw.Filtered {
		s.filtered[id] = struct{}{}
		s.finished[id] = struct{}{}
	}
	s.persistedPath = canonicalPath(path)
	s.persisted = s.revision
	if s.needsScopeSave {
		s.revision++
	}

	return s, nil
}

// Save writes the state atomically.
func (s *State) Save(path string) error {
	if path == "" {
		return nil
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.Lock()
	if s.persisted == s.revision && s.persistedPath == canonicalPath(path) {
		if _, err := os.Stat(path); err == nil {
			s.mu.Unlock()
			return nil
		}
	}
	revision := s.revision
	raw := struct {
		Scope        string              `json:"scope,omitempty"`
		Finished     []int               `json:"finished"`
		Skipped      []int               `json:"skipped"`
		LastTS       int64               `json:"last_ts"`
		MediaRecords map[int]MediaRecord `json:"media,omitempty"`
		Filtered     []int               `json:"filtered,omitempty"`
	}{
		Scope:        s.Scope,
		Finished:     s.finishedIDs(),
		Skipped:      s.skippedIDs(),
		LastTS:       s.LastTS,
		MediaRecords: s.mediaRecords(),
		Filtered:     s.filteredIDs(),
	}
	s.mu.Unlock()

	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "marshal state"), corei18n.Message{ID: "errors.context.marshal_state", Args: map[string]any{"Reason": err}})
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return diagnostic.Describe(errors.Wrapf(err, "create state dir %s", dir), corei18n.Message{ID: "errors.context.create_state_dir_value", Args: map[string]any{"Arg1": dir, "Reason": err}})
		}
	}

	tmp := path + ".new"
	if err = os.WriteFile(tmp, b, 0o644); err != nil {
		return diagnostic.Describe(errors.Wrapf(err, "write state %s", path), corei18n.Message{ID: "errors.context.write_state_value", Args: map[string]any{"Arg1": path, "Reason": err}})
	}

	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	s.mu.Lock()
	s.needsScopeSave = false
	s.persisted = revision
	s.persistedPath = canonicalPath(path)
	s.mu.Unlock()
	return nil
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
		if _, exists := s.finished[id]; !exists {
			s.revision++
		}
		s.finished[id] = struct{}{}
	}
}

// Skip records ids as unavailable.
func (s *State) Skip(ids ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		if _, exists := s.skipped[id]; !exists {
			s.revision++
		}
		delete(s.media, id)
		delete(s.filtered, id)
		s.finished[id] = struct{}{}
		s.skipped[id] = struct{}{}
	}
}

// CompleteMedia records completion only after the payload has been committed.
func (s *State) CompleteMedia(id int, identity downloader.FileIdentity, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "stat committed media"), corei18n.Message{ID: "errors.context.stat_committed_media", Args: map[string]any{"Reason": err}})
	}
	if !info.Mode().IsRegular() || info.Size() != identity.Size {
		return func() error {
			messageArg2 := info.Size()
			return diagnostic.Describe(errors.Errorf("committed media %s has size %d, expected %d", path, messageArg2, identity.Size), corei18n.Message{ID: "errors.message.committed_media_value_has_size_value_expected_value", Args: map[string]any{"Arg1": path, "Arg2": messageArg2, "Arg3": identity.Size}})
		}()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := MediaRecord{Identity: identity, Path: canonicalPath(path), Size: info.Size(), ModTimeNS: info.ModTime().UnixNano()}
	if previous, ok := s.media[id]; !ok || previous != record {
		s.revision++
	}
	s.media[id] = record
	s.finished[id] = struct{}{}
	delete(s.skipped, id)
	delete(s.filtered, id)
	return nil
}

func (s *State) Media(id int) (MediaRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.media[id]
	return record, ok
}

// Filter marks messages excluded by this job's immutable extension selection.
func (s *State) Filter(ids ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if _, ok := s.filtered[id]; !ok {
			s.revision++
		}
		s.filtered[id] = struct{}{}
		s.finished[id] = struct{}{}
		delete(s.media, id)
		delete(s.skipped, id)
	}
}

func (s *State) IsTerminalWithoutMedia(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, skipped := s.skipped[id]
	_, filtered := s.filtered[id]
	return skipped || filtered
}

// ForgetMedia clears an invalid completion record so a failed replacement is
// retried and cannot advance the incremental timestamp.
func (s *State) ForgetMedia(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.finished[id]; ok {
		s.revision++
	}
	delete(s.finished, id)
	delete(s.media, id)
	delete(s.skipped, id)
	delete(s.filtered, id)
}

func (s *State) mediaRecords() map[int]MediaRecord {
	if len(s.media) == 0 {
		return nil
	}
	out := make(map[int]MediaRecord, len(s.media))
	for id, record := range s.media {
		out[id] = record
	}
	return out
}

func (s *State) filteredIDs() []int {
	out := make([]int, 0, len(s.filtered))
	for id := range s.filtered {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
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
	if n > 0 {
		s.revision++
	}
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

	if s.LastTS != ts {
		s.LastTS = ts
		s.revision++
	}
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
func LoadStateStore(path string, scopes ...string) (*stateStore, *State, error) {
	scope := ""
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	st, err := loadState(path, scope)
	if err != nil {
		return nil, nil, err
	}

	store := &stateStore{path: path, state: st}
	// Loading is read-only. Empty legacy documents bind in memory and are
	// persisted only after an explicitly mutating execution succeeds.
	return store, st, nil
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
	defer s.mu.Unlock()
	if time.Since(s.lastSave) < saveInterval {
		return nil
	}
	err := s.state.Save(s.path)
	if err == nil {
		s.lastSave = time.Now()
	}
	return err
}
