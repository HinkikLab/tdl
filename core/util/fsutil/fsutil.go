package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func GetNameWithoutExt(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || os.IsExist(err)
}

// AddPrefixDot add prefix dot if extension don't have
func AddPrefixDot(ext string) string {
	if !strings.HasPrefix(ext, ".") {
		return "." + ext
	}
	return ext
}

// JoinWithin joins a generated relative path to dir and rejects paths that
// would escape the configured output directory.
func JoinWithin(dir, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("generated path is empty")
	}
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("generated path escapes the output directory: %q", name)
	}
	return filepath.Join(dir, clean), nil
}
