package i18n

import (
	"errors"

	"github.com/spf13/pflag"

	"github.com/iyear/tdl/core/diagnostic"
)

// FlagError adapts pflag's typed failures without parsing English messages.
func FlagError(cause error) error {
	var missing *pflag.NotExistError
	var required *pflag.ValueRequiredError
	var invalid *pflag.InvalidValueError
	var syntax *pflag.InvalidSyntaxError
	var result *diagnostic.Error
	switch {
	case errors.As(cause, &missing):
		flag := "--" + missing.GetSpecifiedName()
		if missing.GetSpecifiedShortnames() != "" {
			flag = "-" + missing.GetSpecifiedName()
		}
		result = diagnostic.Wrap("errors.cli.unknown_flag", map[string]any{"Flag": flag}, cause)
	case errors.As(cause, &required):
		result = diagnostic.Wrap("errors.cli.value_required", map[string]any{"Flag": "--" + required.GetFlag().Name}, cause)
	case errors.As(cause, &invalid):
		result = diagnostic.Wrap("errors.cli.invalid_value", map[string]any{"Flag": "--" + invalid.GetFlag().Name, "Value": invalid.GetValue(), "Type": invalid.GetFlag().Value.Type()}, cause)
	case errors.As(cause, &syntax):
		result = diagnostic.Wrap("errors.cli.invalid_syntax", map[string]any{"Flag": syntax.GetSpecifiedFlag()}, cause)
	default:
		return diagnostic.InvalidCLIFlags(cause)
	}
	result.CauseInMessage = true
	return result
}
