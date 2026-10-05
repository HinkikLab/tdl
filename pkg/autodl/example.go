package autodl

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	appi18n "github.com/iyear/tdl/pkg/i18n"
)

//go:embed config.example.yaml
var exampleConfig string

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
	selected, err := corei18n.NormalizeLanguage(language)
	if err != nil {
		return err
	}
	translator, err := appi18n.New(selected)
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("load configuration comment translations: %w", err), corei18n.Message{ID: "errors.message.load_configuration_comment_translations_value", Args: map[string]any{"Arg1": err}})
	}
	content, err := renderExampleConfig(translator)
	if err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return diagnostic.Describe(fmt.Errorf("config path must not be empty"), corei18n.Message{ID: "errors.message.config_path_must_not_be_empty"})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return diagnostic.Describe(fmt.Errorf("create config directory: %w", err), corei18n.Message{ID: "errors.message.create_config_directory_value", Args: map[string]any{"Arg1": err}})
	}
	if !overwrite {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			return diagnostic.Describe(fmt.Errorf("config %s already exists; choose another --config path or use --force to overwrite", path), corei18n.Message{ID: "errors.config.exists", Args: map[string]any{"Arg1": path}})
		}
		if err != nil {
			return diagnostic.Describe(fmt.Errorf("create config %s: %w", path, err), corei18n.Message{ID: "errors.message.create_config_value_value", Args: map[string]any{"Arg1": path, "Arg2": err}})
		}
		_, writeErr := f.WriteString(content)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			if writeErr != nil {
				return diagnostic.Describe(fmt.Errorf("write config %s: %w", path, writeErr), corei18n.Message{ID: "errors.message.write_config_value_value", Args: map[string]any{"Arg1": path, "Arg2": writeErr}})
			}
			return diagnostic.Describe(fmt.Errorf("close config %s: %w", path, closeErr), corei18n.Message{ID: "errors.message.close_config_value_value", Args: map[string]any{"Arg1": path, "Arg2": closeErr}})
		}
		return nil
	}

	// Finish writing before replacing an existing configuration.
	f, err := os.CreateTemp(filepath.Dir(path), ".tdl-config-*")
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("create temporary config: %w", err), corei18n.Message{ID: "errors.message.create_temporary_config_value", Args: map[string]any{"Arg1": err}})
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0o644); err == nil {
		_, err = f.WriteString(content)
	}
	closeErr := f.Close()
	if err != nil {
		return diagnostic.Describe(fmt.Errorf("write config %s: %w", path, err), corei18n.Message{ID: "errors.message.write_config_value_value", Args: map[string]any{"Arg1": path, "Arg2": err}})
	}
	if closeErr != nil {
		return diagnostic.Describe(fmt.Errorf("close config %s: %w", path, closeErr), corei18n.Message{ID: "errors.message.close_config_value_value", Args: map[string]any{"Arg1": path, "Arg2": closeErr}})
	}
	if err = os.Rename(tmp, path); err != nil {
		return diagnostic.Describe(fmt.Errorf("replace config %s: %w", path, err), corei18n.Message{ID: "errors.message.replace_config_value_value", Args: map[string]any{"Arg1": path, "Arg2": err}})
	}
	return nil
}

func renderExampleConfig(translator corei18n.Translator) (string, error) {
	var output strings.Builder
	for _, line := range strings.SplitAfter(exampleConfig, "\n") {
		body := strings.TrimSuffix(line, "\n")
		ending := line[len(body):]
		comment := strings.Index(body, "# @i18n:")
		if comment < 0 {
			output.WriteString(line)
			continue
		}
		id := strings.TrimSpace(body[comment+len("# @i18n:"):])
		if id == "" {
			return "", diagnostic.Describe(fmt.Errorf("empty translation ID in example config"), corei18n.Message{ID: "errors.message.empty_translation_id_in_example_config"})
		}
		text := translator.Translate(corei18n.Message{ID: id})
		if text == id {
			return "", diagnostic.Describe(fmt.Errorf("missing translation %q for example config", id), corei18n.Message{ID: "errors.message.missing_translation_value_for_example_config", Args: map[string]any{"Arg1": fmt.Sprintf("%q", id)}})
		}
		// Every translated line must remain a comment, including when a future
		// catalog entry spans several lines next to a configuration value.
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
		text = strings.ReplaceAll(text, "\n", "\n# ")
		output.WriteString(body[:comment])
		output.WriteString("# ")
		output.WriteString(text)
		output.WriteString(ending)
	}
	return output.String(), nil
}

// ExampleConfigCommentIDs returns the stable IDs referenced by the shared
// YAML template for resource coverage checks.
func ExampleConfigCommentIDs() []string {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(exampleConfig, "\n") {
		marker := strings.Index(line, "# @i18n:")
		if marker < 0 {
			continue
		}
		seen[strings.TrimSpace(line[marker+len("# @i18n:"):])] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
