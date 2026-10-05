package autodl

import (
	"fmt"
	"os"
	"strings"
)

// ResolveExampleLanguage resolves auto from the locale environment, then the
// Windows UI language. Other languages and unavailable locales use English.
func ResolveExampleLanguage(language string) (string, error) {
	language = strings.ToLower(strings.TrimSpace(language))
	switch language {
	case "zh", "en":
		return language, nil
	case "", "auto":
		return detectExampleLanguage(os.Getenv, systemExampleLocale), nil
	default:
		return "", fmt.Errorf("unsupported config language %q; use auto, zh or en", language)
	}
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
