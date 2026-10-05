package i18n

import (
	"fmt"
	"strings"
)

// Language is the canonical language selected for one program run.
type Language string

const (
	English Language = "en"
	Chinese Language = "zh"
)

// NormalizeLanguage maps supported language tags to the languages shipped by
// tdl. All Chinese regions and scripts intentionally use Simplified Chinese.
func NormalizeLanguage(value string) (Language, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "auto" {
		return "", fmt.Errorf("language must be auto, zh or en")
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '@'
	})
	if len(parts) == 0 {
		return "", fmt.Errorf("language must be auto, zh or en")
	}
	switch parts[0] {
	case "zh":
		return Chinese, nil
	case "en", "c", "posix":
		return English, nil
	default:
		return "", fmt.Errorf("unsupported language %q; use auto, zh or en", value)
	}
}

// ResolveLanguage applies explicit language, TDL_LANGUAGE, locale environment,
// system UI language, then English. An explicit "auto" requests detection.
func ResolveLanguage(explicit string, getenv func(string) string, systemLocale func() string) (Language, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" && !strings.EqualFold(explicit, "auto") {
		return NormalizeLanguage(explicit)
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if v := strings.TrimSpace(getenv("TDL_LANGUAGE")); v != "" && !strings.EqualFold(v, "auto") {
		if lang, err := NormalizeLanguage(v); err == nil {
			return lang, nil
		}
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if locale := strings.TrimSpace(getenv(key)); locale != "" {
			return localeLanguage(locale), nil
		}
	}
	if systemLocale != nil {
		if locale := strings.TrimSpace(systemLocale()); locale != "" {
			return localeLanguage(locale), nil
		}
	}
	return English, nil
}

func localeLanguage(locale string) Language {
	for _, preference := range strings.Split(strings.ToLower(locale), ":") {
		parts := strings.FieldsFunc(strings.TrimSpace(preference), func(r rune) bool {
			return r == '-' || r == '_' || r == '.' || r == '@'
		})
		if len(parts) == 0 {
			continue
		}
		switch parts[0] {
		case "zh":
			return Chinese
		case "en", "c", "posix":
			return English
		default:
			// LANGUAGE is an ordered preference list; skip unsupported entries.
			if strings.Contains(locale, ":") {
				continue
			}
			return English
		}
	}
	return English
}
