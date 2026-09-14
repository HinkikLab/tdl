package downloader

import (
	"encoding/json"
	"os"
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
	// Version rejects journals produced by the old truncating resume path.
	Version int `json:"version"`
	// Parts is the total number of parts of the file.
	Parts int `json:"parts"`
	// Size is the size of the file the parts belong to. It is used to detect
	// that the media changed on the server, which invalidates the parts.
	Size int64 `json:"size"`
	// Done holds the indexes of the parts that are fully written to disk.
	Done []int `json:"done"`
}

// PartsStore tracks which parts of one element already hit the disk.
//
// All methods are safe for concurrent use.
type PartsStore struct {
	path string
	size int64

	mu       sync.Mutex
	done     map[int]struct{}
	dirty    int
	lastSave time.Time
}

// NewPartsStore loads the parts of the file at tempPath.
//
// size is the current size reported by Telegram. A sidecar that does not match
// it is ignored and removed, because the remote media changed.
func NewPartsStore(tempPath string, size int64) *PartsStore {
	s := &PartsStore{
		path: PartsPath(tempPath),
		size: size,
		done: make(map[int]struct{}),
	}

	b, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}

	var f partsFile
	if err = json.Unmarshal(b, &f); err != nil || f.Version != 1 || f.Size != size || f.Parts != PartsCount(size) {
		_ = os.Remove(s.path)
		return s
	}

	stat, err := os.Stat(tempPath)
	if err != nil {
		_ = os.Remove(s.path)
		return s
	}
	for _, i := range f.Done {
		if i >= 0 && i < f.Parts && min(int64(i+1)*MaxPartSize, size) <= stat.Size() {
			s.done[i] = struct{}{}
		}
	}

	return s
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
	s.Reset()
}

// flush writes the sidecar. The caller must hold the lock.
func (s *PartsStore) flush() error {
	if s.dirty == 0 {
		return nil
	}
	f := partsFile{
		Version: 1,
		Parts:   PartsCount(s.size),
		Size:    s.size,
		Done:    make([]int, 0, len(s.done)),
	}
	for i := range s.done {
		f.Done = append(f.Done, i)
	}

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

// OpenPartial preserves validated completed parts, discarding stale data when
// no usable journal exists. Never truncate before loading the journal.
func OpenPartial(path string, size int64) (*os.File, *PartsStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, err
	}
	s := NewPartsStore(path, size)
	if len(s.Done()) == 0 {
		s.Reset()
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
