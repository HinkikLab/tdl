package autodl

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed config.example.yaml
var exampleConfig string

//go:embed config.example.zh.yaml
var exampleConfigZH string

// WriteExampleConfig writes the annotated, multi-mode batch examples without
// loading an account or contacting Telegram. Existing files require overwrite.
func WriteExampleConfig(path string, overwrite bool) error {
	return WriteExampleConfigLanguage(path, overwrite, "auto")
}

// WriteExampleConfigLanguage selects Chinese or English comments, leaving the
// configuration values identical. Language may be auto, zh or en.
func WriteExampleConfigLanguage(path string, overwrite bool, language string) error {
	language, err := ResolveExampleLanguage(language)
	if err != nil {
		return err
	}
	content := exampleConfig
	if language == "zh" {
		content = exampleConfigZH
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if !overwrite {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			return fmt.Errorf("config %s already exists; choose another --config path or use --force to overwrite", path)
		}
		if err != nil {
			return fmt.Errorf("create config %s: %w", path, err)
		}
		_, writeErr := f.WriteString(content)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			if writeErr != nil {
				return fmt.Errorf("write config %s: %w", path, writeErr)
			}
			return fmt.Errorf("close config %s: %w", path, closeErr)
		}
		return nil
	}

	// Finish writing before replacing an existing configuration.
	f, err := os.CreateTemp(filepath.Dir(path), ".tdl-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0o644); err == nil {
		_, err = f.WriteString(content)
	}
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close config %s: %w", path, closeErr)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace config %s: %w", path, err)
	}
	return nil
}
