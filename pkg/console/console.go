package console

import (
	"context"
	"fmt"
	"io"

	surveyterm "github.com/AlecAivazis/survey/v2/terminal"
	"github.com/fatih/color"
	"go.etcd.io/bbolt"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

// Translate renders a message through the translator bound to ctx.
func Translate(ctx context.Context, message corei18n.Message) string {
	if translator := corei18n.FromContext(ctx); translator != nil {
		return translator.Translate(message)
	}
	return corei18n.EnglishTranslator().Translate(message)
}

// Info writes one translated line to out.
func Info(out io.Writer, translator corei18n.Translator, message corei18n.Message) {
	if translator == nil {
		translator = corei18n.EnglishTranslator()
	}
	_, _ = fmt.Fprintln(out, translator.Translate(message))
}

// FormatError localizes structured project diagnostics and well-known external
// error types while preserving unrecognized causes verbatim.
func FormatError(err error, translator corei18n.Translator) string {
	return diagnostic.FormatErrorWith(err, translator, func(err error, translator corei18n.Translator) (string, bool) {
		switch err {
		case bbolt.ErrTimeout:
			return translator.Translate(corei18n.Message{ID: "errors.storage.in_use", Default: "Current database is used by another process, please terminate it first."}), true
		case surveyterm.InterruptErr:
			return translator.Translate(corei18n.Message{ID: "errors.interrupted", Default: "Interrupted."}), true
		default:
			return "", false
		}
	})
}

// PrintError writes one localized terminal error and optional stable debug
// details. The returned string is also suitable for non-colored callers.
func PrintError(w io.Writer, err error, translator corei18n.Translator, debug bool) string {
	message := FormatError(err, translator)
	if translator == nil {
		translator = corei18n.EnglishTranslator()
	}
	_, _ = color.New(color.FgRed, color.Bold).Fprint(w, translator.Translate(corei18n.Message{ID: "console.error.prefix", Default: "Error: "}))
	_, _ = fmt.Fprintln(w, message)
	if debug {
		_, _ = fmt.Fprintf(w, "\n%s\n%+v\n", translator.Translate(corei18n.Message{ID: "console.error.debug_details", Default: "Debug details:"}), err)
	}
	return message
}
