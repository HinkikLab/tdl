package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// ValidateNamespace prevents optional extension catalogs from defining another
// component's messages. Full resource validation still runs during loading.
func ValidateNamespace(prefix string, resources ...fs.FS) error {
	for _, resource := range resources {
		if err := fs.WalkDir(resource, ".", func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.EqualFold(path.Ext(file), ".json") {
				return err
			}
			data, err := fs.ReadFile(resource, file)
			if err != nil {
				return err
			}
			var messages map[string]json.RawMessage
			if err := json.Unmarshal(data, &messages); err != nil {
				return err
			}
			for id := range messages {
				if !strings.HasPrefix(id, prefix) {
					return fmt.Errorf("translation ID %q must use namespace %q", id, prefix)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
