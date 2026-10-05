package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
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
		return "", diagnostic.Describe(fmt.Errorf("generated path is empty"), corei18n.Message{ID: "errors.message.generated_path_is_empty"})
	}
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", diagnostic.Describe(fmt.Errorf("generated path escapes the output directory: %q", name), corei18n.Message{ID: "errors.message.generated_path_escapes_the_output_directory_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", name)}})
	}
	return filepath.Join(dir, clean), nil
}
