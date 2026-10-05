// Package transfer owns the local file lifecycle shared by download modes.
package transfer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/downloader"
)

// Reservations keeps ownership for the entire run, including completed files.
// This prevents a later media item from overwriting an earlier item's target.
type Reservations struct {
	mu    sync.Mutex
	paths map[string]string
}

func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return abs, nil
}

func (r *Reservations) Reserve(path, owner string) (string, error) {
	abs, err := CanonicalPath(path)
	if err != nil {
		return "", err
	}
	key := abs
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paths == nil {
		r.paths = make(map[string]string)
	}
	keys := []string{key, key + ".tmp", key + ".tmp.parts", key + ".tmp.parts.new"}
	for _, candidate := range keys {
		if previous, ok := r.paths[candidate]; ok && previous != owner {
			return "", fmt.Errorf("output path %q is already reserved by %s (new item: %s)", abs, previous, owner)
		}
	}
	for _, candidate := range keys {
		r.paths[candidate] = owner
	}
	return abs, nil
}

// Commit always flushes and closes after workers settle. It returns only local
// lifecycle errors; the caller retains the original download error. Failed
// downloads and failed commits leave their partial file and journal intact.
func Commit(file *os.File, parts *downloader.PartsStore, size int64, target string, date int64, downloadErr error) error {
	if file == nil {
		return fmt.Errorf("download file was not opened")
	}
	var err error
	if parts != nil {
		err = multierr.Append(err, parts.Flush())
	}
	err = multierr.Append(err, file.Close())
	if err != nil || downloadErr != nil {
		return err
	}
	stat, err := os.Stat(file.Name())
	if err != nil {
		return fmt.Errorf("stat partial file: %w", err)
	}
	if !stat.Mode().IsRegular() || stat.Size() != size {
		return fmt.Errorf("downloaded file size mismatch: got %d bytes, expected %d", stat.Size(), size)
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return fmt.Errorf("commit downloaded file: %w", err)
	}
	if parts != nil {
		if err := parts.RemoveChecked(); err != nil {
			return fmt.Errorf("remove part journal: %w", err)
		}
	}
	if date > 0 {
		ts := time.Unix(date, 0)
		// Timestamp metadata is best effort after payload commit.
		_ = os.Chtimes(target, ts, ts)
	}
	return nil
}
