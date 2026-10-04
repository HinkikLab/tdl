package chat

import (
	"encoding/json"
	"os"
	"path/filepath"

	"go.uber.org/multierr"
)

const archiveMetadataName = "meta.json"

// readArchiveMetadata retains compatibility with archives created before the
// metadata rename. A present meta.json always takes precedence.
func readArchiveMetadata(dir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, archiveMetadataName))
	if os.IsNotExist(err) {
		return os.ReadFile(filepath.Join(dir, "message.json"))
	}
	return b, err
}

func writeArchiveMetadata(dir string, value any, enabled *bool) error {
	if !defaultOn(enabled) {
		return nil
	}
	return writeArchiveJSON(filepath.Join(dir, archiveMetadataName), value)
}

func writeArchiveJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".meta-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, writeErr := f.Write(append(b, '\n'))
	closeErr := f.Close()
	if err := multierr.Combine(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(name, path)
}
