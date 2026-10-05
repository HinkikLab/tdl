package autodl

import "golang.org/x/sys/windows"

func systemExampleLocale() string {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err == nil && len(languages) > 0 {
		return languages[0]
	}
	return ""
}
