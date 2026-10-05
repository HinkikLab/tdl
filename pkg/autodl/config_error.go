package autodl

import (
	"fmt"
	"strings"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// ConfigError keeps actionable configuration context separate from call stacks.
type ConfigError struct {
	Path    string
	Job     int // one-based; zero denotes a top-level error
	ChatURL string
	Err     error
}

// i18n:ignore Stable English debug context; presentation uses FormatLocalized.
func (e *ConfigError) context() string {
	var b strings.Builder
	if e.Path != "" {
		fmt.Fprintf(&b, "config \"%s\" is invalid", e.Path)
	} else {
		b.WriteString("invalid batch configuration")
	}
	if e.Job > 0 {
		fmt.Fprintf(&b, "\n  Job: %d", e.Job)
	}
	if e.ChatURL != "" {
		fmt.Fprintf(&b, "\n  Chat: %s", e.ChatURL)
	}
	return b.String()
}

func (e *ConfigError) Error() string {
	return e.FormatLocalized(corei18n.EnglishTranslator())
}

// FormatLocalized renders stable configuration context and a separately
// localized reason for command-line presentation.
func (e *ConfigError) FormatLocalized(translator corei18n.Translator) string {
	if translator == nil {
		translator = corei18n.EnglishTranslator()
	}
	message := translator.Translate(corei18n.Message{ID: "errors.config.invalid", Default: "invalid batch configuration"})
	if e.Path != "" {
		message = translator.Translate(corei18n.Message{ID: "errors.config.invalid_path", Args: map[string]any{"Path": e.Path}, Default: "config \"{{.Path}}\" is invalid"})
	}
	if e.Job > 0 {
		message += "\n  " + translator.Translate(corei18n.Message{ID: "errors.config.job", Args: map[string]any{"Job": e.Job}, Default: "Job: {{.Job}}"})
	}
	if e.ChatURL != "" {
		message += "\n  " + translator.Translate(corei18n.Message{ID: "errors.config.chat", Args: map[string]any{"Chat": e.ChatURL}, Default: "Chat: {{.Chat}}"})
	}
	if e.Err != nil {
		reason := diagnostic.FormatError(e.Err, translator)
		message += "\n  " + translator.Translate(corei18n.Message{ID: "errors.config.reason", Default: "Reason"}) + ": " + strings.ReplaceAll(reason, "\n", "\n          ")
	}
	return message
}

func (e *ConfigError) Unwrap() error { return e.Err }

// Format preserves the cause's source information when --debug requests %+v.
func (e *ConfigError) Format(s fmt.State, verb rune) {
	if verb == 'v' && s.Flag('+') {
		// i18n:ignore Debug details preserve English context and source stacks.
		fmt.Fprintf(s, "%s\n  Cause:\n%+v", e.context(), e.Err)
		return
	}
	fmt.Fprint(s, e.Error())
}
