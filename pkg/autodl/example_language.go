package autodl

import (
	"fmt"
	"os"
	"strings"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// ResolveExampleLanguage resolves auto from the locale environment, then the
// Windows UI language. Other languages and unavailable locales use English.
func ResolveExampleLanguage(language string) (string, error) {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" || language == "auto" {
		selected, err := corei18n.ResolveLanguage("", os.Getenv, systemExampleLocale)
		if err != nil {
			return "", err
		}
		return string(selected), nil
	}
	selected, err := corei18n.NormalizeLanguage(language)
	if err != nil {
		return "", diagnostic.Describe(fmt.Errorf("unsupported config language %q; use auto, zh or en", language), corei18n.Message{ID: "errors.message.unsupported_config_language_value_use_auto_zh_or_en", Args: map[string]any{"Arg1": fmt.Sprintf("%q", language)}})
	}
	return string(selected), nil
}

func detectExampleLanguage(getenv func(string) string, systemLocale func() string) string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if locale := strings.TrimSpace(getenv(key)); locale != "" {
			return exampleLocaleLanguage(locale)
		}
	}
	return exampleLocaleLanguage(systemLocale())
}

func exampleLocaleLanguage(locale string) string {
	// LANGUAGE can contain an ordered list of preferred languages.
	for _, preference := range strings.Split(strings.ToLower(locale), ":") {
		base := strings.FieldsFunc(strings.TrimSpace(preference), func(r rune) bool {
			return r == '-' || r == '_' || r == '.' || r == '@'
		})
		if len(base) > 0 {
			switch base[0] {
			case "zh", "en":
				return base[0]
			case "c", "posix":
				return "en"
			}
		}
	}
	return "en"
}
