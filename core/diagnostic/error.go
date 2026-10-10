package diagnostic

import (
	"errors"
	"fmt"
	"strings"

	"github.com/iyear/tdl/core/i18n"
)

// Error preserves stable machine-readable semantics independently from the
// language used by a terminal renderer.
type Error struct {
	Code  string
	Args  map[string]any
	Cause error
	Kind  error
	// Original preserves the exact legacy error and its debug information.
	// The descriptor is used only when rendering user-facing text.
	Original bool
	Default  string
	// CauseInMessage means the localized message already explains the cause.
	// Unwrap and the stable Error string still preserve that cause.
	CauseInMessage bool
}

// Formattable errors expose a structured terminal representation without
// changing their stable Go Error() string.
type Formattable interface {
	FormatLocalized(i18n.Translator) string
}

func New(code string, args map[string]any) *Error {
	return &Error{Code: code, Args: clone(args)}
}

func Wrap(code string, args map[string]any, cause error) *Error {
	return &Error{Code: code, Args: clone(args), Cause: cause}
}

// Describe attaches a language-independent descriptor to an existing error.
// Error, Unwrap and debug formatting retain the original diagnostic. Error
// values in message arguments are localized recursively during presentation.
func Describe(original error, message i18n.Message) error {
	if original == nil {
		return nil
	}
	return &Error{
		Code: message.ID, Args: clone(message.Args), Default: message.Default,
		Cause: original, CauseInMessage: true, Original: true,
	}
}

func WithKind(err *Error, kind error) *Error {
	if err == nil {
		return nil
	}
	copy := *err
	copy.Kind = kind
	return &copy
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Original && e.Cause != nil {
		return e.Cause.Error()
	}
	message := i18n.EnglishTranslator().Translate(i18n.Message{ID: e.Code, Args: e.Args})
	if e.Cause != nil {
		if message == e.Code || message == "" {
			return e.Cause.Error()
		}
		return message + ": " + e.Cause.Error()
	}
	return message
}

func (e *Error) Format(state fmt.State, verb rune) {
	if verb == 'v' && state.Flag('+') && e != nil && e.Cause != nil {
		if e.Original {
			fmt.Fprintf(state, "%+v", e.Cause)
		} else {
			fmt.Fprintf(state, "%s\n%+v", e.Error(), e.Cause)
		}
		return
	}
	if verb == 'q' {
		fmt.Fprintf(state, "%q", e.Error())
		return
	}
	fmt.Fprint(state, e.Error())
}

func (e *Error) FormatLocalized(translator i18n.Translator) string {
	return FormatError(e, translator)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	return e.Kind != nil && errors.Is(e.Kind, target)
}

// FormatError renders every branch while retaining legacy wrapper context.
func FormatError(err error, translator i18n.Translator) string {
	return FormatErrorWith(err, translator, nil)
}

// FormatErrorWith allows the application to adapt its external error types
// without introducing application dependencies into core.
func FormatErrorWith(err error, translator i18n.Translator, adapt func(error, i18n.Translator) (string, bool)) string {
	if err == nil {
		return ""
	}
	if translator == nil {
		translator = i18n.EnglishTranslator()
	}
	if structured, ok := err.(*Error); ok {
		if structured == nil {
			return ""
		}
		args := structured.Args
		containsCause := structured.CauseInMessage
		args = clone(args)
		for name, value := range args {
			if cause, ok := value.(error); ok {
				args[name] = FormatErrorWith(cause, translator, adapt)
			} else if message, ok := value.(i18n.Message); ok {
				args[name] = translator.Translate(message)
			}
		}
		if reason, ok := args["Reason"].(string); ok && structured.Cause != nil && reason == structured.Cause.Error() {
			args = clone(args)
			args["Reason"] = FormatErrorWith(structured.Cause, translator, adapt)
			containsCause = true
		}
		message := translator.Translate(i18n.Message{ID: structured.Code, Args: args, Default: structured.Default})
		if structured.Cause != nil && (message == structured.Code || message == "") {
			return FormatErrorWith(structured.Cause, translator, adapt)
		}
		if structured.Cause != nil && !containsCause {
			cause := FormatErrorWith(structured.Cause, translator, adapt)
			if message == structured.Code || message == "" {
				return cause
			}
			return message + ": " + cause
		}
		return message
	}
	if formattable, ok := err.(Formattable); ok {
		return formattable.FormatLocalized(translator)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		original, remainder := err.Error(), err.Error()
		var rendered strings.Builder
		replacedAll := true
		for _, cause := range joined.Unwrap() {
			if cause != nil {
				localized := FormatErrorWith(cause, translator, adapt)
				parts = append(parts, localized)
				position := strings.Index(remainder, cause.Error())
				if position < 0 {
					replacedAll = false
					continue
				}
				rendered.WriteString(remainder[:position])
				rendered.WriteString(localized)
				remainder = remainder[position+len(cause.Error()):]
			}
		}
		if replacedAll {
			rendered.WriteString(remainder)
			return rendered.String()
		}
		return original + "\n" + strings.Join(parts, "\n")
	}
	if adapt != nil {
		if text, ok := adapt(err, translator); ok {
			return text
		}
	}
	if text, ok := formatExternal(err, translator); ok {
		return text
	}
	if cause := errors.Unwrap(err); cause != nil {
		// fmt.Errorf and go-faster/errors append the original cause verbatim.
		// Preserve the prefix, replacing only that known suffix.
		original := err.Error()
		if strings.HasSuffix(original, cause.Error()) {
			return strings.TrimSuffix(original, cause.Error()) + FormatErrorWith(cause, translator, adapt)
		}
	}
	return err.Error()
}

func InvalidRange(start, end int) *Error {
	return New("errors.config.invalid_range", map[string]any{"Start": start, "End": end})
}

func InvalidCLIFlags(cause error) *Error {
	if cause == nil {
		return New("errors.cli.invalid_flags_generic", nil)
	}
	err := Wrap("errors.cli.invalid_flags", map[string]any{"Reason": cause.Error()}, cause)
	err.CauseInMessage = true
	return err
}

func InvalidCLIArguments(cause error) *Error {
	if cause == nil {
		return New("errors.cli.invalid_arguments_generic", nil)
	}
	err := Wrap("errors.cli.invalid_arguments", map[string]any{"Reason": cause.Error()}, cause)
	err.CauseInMessage = true
	return err
}

func clone(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	return out
}
