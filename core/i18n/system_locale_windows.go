package i18n

import "golang.org/x/sys/windows"

// SystemLocale reports the operating system's preferred UI language.
func SystemLocale() string {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err == nil && len(languages) > 0 {
		return languages[0]
	}
	return ""
}
