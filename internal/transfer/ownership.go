package transfer

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// ReserveState protects a state snapshot and its atomic replacement from media
// output paths, including media temporary files and part journals.
func (r *Reservations) ReserveState(path, owner string) error {
	abs, err := CanonicalPath(path)
	if err != nil {
		return err
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
	keys := []string{key, key + ".new"}
	for _, candidate := range keys {
		if previous, ok := r.paths[candidate]; ok && previous != owner {
			return diagnostic.Describe(fmt.Errorf("state path %q conflicts with %s (new owner: %s)", abs, previous, owner), corei18n.Message{ID: "errors.transfer.state_path_value_conflicts_with_value_new_owner_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", abs), "Arg2": previous, "Arg3": owner}})
		}
	}
	for _, candidate := range keys {
		r.paths[candidate] = owner
	}
	return nil
}

// PreserveFile keeps a known incompatible final payload without overwriting an
// earlier recovery file. Only the explicitly named regular file is moved.
func PreserveFile(path string) (string, error) {
	stat, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !stat.Mode().IsRegular() {
		return "", diagnostic.Describe(fmt.Errorf("cannot preserve non-regular payload %q", path), corei18n.Message{ID: "errors.transfer.cannot_preserve_non_regular_payload_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", path)}})
	}
	for number := 0; ; number++ {
		backup := path + ".unverified"
		if number > 0 {
			backup += fmt.Sprintf(".%d", number)
		}
		if _, err := os.Lstat(backup); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.Rename(path, backup); err != nil {
			return "", err
		}
		return backup, nil
	}
}
