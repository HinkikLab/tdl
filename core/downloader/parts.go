package downloader

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// PartsExt is the extension of the sidecar file holding the parts of a
// partially downloaded element.
//
// It is written next to the temp file, so "1_2_video.mp4.tmp.parts" holds the
// parts of "1_2_video.mp4.tmp". A restarted download then only has to fetch the
// parts that are still missing instead of starting the element from scratch.
const PartsExt = ".parts"

// PartsPath returns the sidecar path of the given temp file.
func PartsPath(tempPath string) string {
	return tempPath + PartsExt
}

// partsFile is the on-disk representation of the download progress of one
// element.
type partsFile struct {
	// Version 2 binds parts to media identity. Version 1 only checked size.
	Version int `json:"version"`
	// Parts is the total number of parts of the file.
	Parts int `json:"parts"`
	// Size is the size of the file the parts belong to. It is used to detect
	// that the media changed on the server, which invalidates the parts.
	Size     int64        `json:"size"`
	Identity FileIdentity `json:"identity"`
	// Done holds the indexes of the parts that are fully written to disk.
	Done []int `json:"done"`
}

// PartsStore tracks which parts of one element already hit the disk.
//
// All methods are safe for concurrent use.
type PartsStore struct {
	path      string
	size      int64
	identity  FileIdentity
	loadErr   error
	rejection string
	recovery  []string

	mu       sync.Mutex
	done     map[int]struct{}
	dirty    int
	lastSave time.Time
}

// NewPartsStore loads the parts of the file at tempPath.
//
// Loading is read-only. An absent identity never authorizes byte reuse. Use
// OpenPartialFile to preserve unverified data and begin a fresh download.
func NewPartsStore(tempPath string, size int64, identities ...FileIdentity) *PartsStore {
	s := &PartsStore{
		path:     PartsPath(tempPath),
		size:     size,
		done:     make(map[int]struct{}),
		lastSave: time.Now(),
	}
	if len(identities) == 1 {
		s.identity = identities[0]
	}

	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.loadErr = err
		}
		return s
	}

	var f partsFile
	if err = json.Unmarshal(b, &f); err != nil {
		s.rejection = "invalid part journal"
		return s
	}
	if f.Version != 2 || !s.identity.valid() || s.identity.Size != size || f.Identity != s.identity || f.Size != size || f.Parts != PartsCount(size) {
		s.rejection = "part journal has an unverified or different file identity"
		return s
	}

	stat, err := os.Stat(tempPath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.loadErr = err
		}
		s.rejection = "part journal has no data file"
		return s
	}
	if !stat.Mode().IsRegular() {
		s.loadErr = fmt.Errorf("partial path %q is not a regular file", tempPath)
		return s
	}
	for _, i := range f.Done {
		if i >= 0 && i < f.Parts && min(int64(i+1)*MaxPartSize, size) <= stat.Size() {
			s.done[i] = struct{}{}
		}
	}

	return s
}

// RecoveryPaths lists previous data and journals preserved by OpenPartial.
// Callers should report these paths so the user can inspect or remove them.
func (s *PartsStore) RecoveryPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.recovery...)
}

// Done returns a copy of the finished part indexes.
func (s *PartsStore) Done() map[int]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[int]struct{}, len(s.done))
	for i := range s.done {
		out[i] = struct{}{}
	}
	return out
}

// PartDone checkpoints at most every 32 new parts or one second of writes.
// Call Flush after workers settle to preserve the last incomplete checkpoint.
func (s *PartsStore) PartDone(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= PartsCount(s.size) {
		return
	}
	if _, ok := s.done[index]; ok {
		return
	}
	s.done[index] = struct{}{}
	s.dirty++
	if s.dirty >= 32 || time.Since(s.lastSave) >= time.Second {
		_ = s.flush()
	}
}

// Flush persists pending parts. It is safe to call after any download outcome.
func (s *PartsStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flush()
}

// Reset drops all tracked parts and the sidecar.
func (s *PartsStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.done = make(map[int]struct{})
	s.dirty = 0
	_ = os.Remove(s.path)
}

// Remove drops the sidecar, e.g. after the element finished successfully.
func (s *PartsStore) Remove() {
	_ = s.RemoveChecked()
}

// RemoveChecked removes a committed file's journal and reports filesystem
// failures. Tracking is retained on failure so the caller can retry cleanup.
func (s *PartsStore) RemoveChecked() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.done = make(map[int]struct{})
	s.dirty = 0
	return nil
}

// flush writes the sidecar. The caller must hold the lock.
func (s *PartsStore) flush() error {
	if s.dirty == 0 {
		return nil
	}
	f := partsFile{
		Version:  2,
		Parts:    PartsCount(s.size),
		Size:     s.size,
		Identity: s.identity,
		Done:     make([]int, 0, len(s.done)),
	}
	for i := range s.done {
		f.Done = append(f.Done, i)
	}
	sort.Ints(f.Done)

	b, err := json.Marshal(f)
	if err != nil {
		return err
	}

	// write to a temp file first so an interrupted flush can't corrupt the
	// sidecar
	tmp := s.path + ".new"
	if err = os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = 0
	s.lastSave = time.Now()
	return nil
}

// OpenPartial preserves validated completed parts. Old, invalid or conflicting
// journals and their bytes are moved together to a unique .unverified path.
// The optional identity keeps the old API source-compatible; without it no
// previous bytes can be trusted. New callers should use OpenPartialFile.
func OpenPartial(path string, size int64, identities ...FileIdentity) (*os.File, *PartsStore, error) {
	if size < 0 || len(identities) > 1 {
		return nil, nil, fmt.Errorf("invalid partial download size or identity count")
	}
	if len(identities) == 1 && (!identities[0].valid() || identities[0].Size != size) {
		return nil, nil, fmt.Errorf("invalid partial file identity")
	}
	s := NewPartsStore(path, size, identities...)
	if s.loadErr != nil {
		return nil, nil, s.loadErr
	}
	stat, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	if stat != nil && !stat.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("partial path %q is not a regular file", path)
	}
	// An orphan nonempty temp file is just as unverified as a legacy journal.
	if s.rejection != "" || (len(s.done) == 0 && stat != nil && stat.Size() > 0) {
		s.recovery, err = preservePartial(path)
		if err != nil {
			return nil, nil, err
		}
		s.done = make(map[int]struct{})
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, err
	}
	if len(s.Done()) == 0 {
		err = f.Truncate(0)
	} else {
		err = f.Truncate(size)
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, s, nil
}

// preservePartial retains the original recovery pair and never overwrites an
// earlier recovery. If moving the journal fails, restore the original data.
func preservePartial(path string) ([]string, error) {
	paths := []string{path, PartsPath(path)}
	var present []bool
	for _, p := range paths {
		_, err := os.Stat(p)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		present = append(present, err == nil)
	}
	for n := 0; ; n++ {
		target := path + ".unverified"
		if n > 0 {
			target += fmt.Sprintf(".%d", n)
		}
		destinations := []string{target, PartsPath(target)}
		available := true
		for _, p := range destinations {
			if _, err := os.Stat(p); err == nil {
				available = false
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		if !available {
			continue
		}
		var moved []string
		for i, p := range paths {
			if !present[i] {
				continue
			}
			if err := os.Rename(p, destinations[i]); err != nil {
				if i > 0 && present[0] {
					if rollbackErr := os.Rename(destinations[0], paths[0]); rollbackErr != nil {
						return nil, fmt.Errorf("preserve partial: %w; restore data: %v", err, rollbackErr)
					}
				}
				return nil, fmt.Errorf("preserve partial %q: %w", p, err)
			}
			moved = append(moved, destinations[i])
		}
		return moved, nil
	}
}

// PreAllocate extends an existing temp file to the expected size so that
// downloading the missing parts can write at their final offsets.
func PreAllocate(f *os.File, size int64) error {
	stat, err := f.Stat()
	if err != nil {
		return err
	}

	if stat.Size() >= size {
		return nil
	}

	return f.Truncate(size)
}

// PartsCount returns how many parts of MaxPartSize a file of the given size
// is split into.
func PartsCount(size int64) int {
	if size <= 0 {
		return 0
	}
	return int((size + MaxPartSize - 1) / MaxPartSize)
}
