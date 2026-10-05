//go:build !windows

package i18n

// SystemLocale is empty when no portable OS display-language API is
// available. Locale environment variables remain the primary non-Windows
// source.
func SystemLocale() string { return "" }
