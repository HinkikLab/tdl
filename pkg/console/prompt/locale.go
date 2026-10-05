// Package prompt adapts Survey's four prompts to immutable per-run languages.
// Templates are translated before rendering so data and cursor widths are
// preserved. It never changes Survey's process-wide template variables.
package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/AlecAivazis/survey/v2"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

type (
	PromptConfig = survey.PromptConfig
	Icon         = survey.Icon
)

type localizedError string

func (e localizedError) Error() string { return string(e) }

func (r *Renderer) setTranslator(t corei18n.Translator) { r.translator = t }

func (r *Renderer) text(id, english string) string {
	t := r.translator
	if t == nil {
		t = corei18n.EnglishTranslator()
	}
	return t.Translate(corei18n.Message{ID: id, Default: english})
}

func (r *Renderer) localizeTemplate(tmpl string) string {
	for _, message := range []corei18n.Message{
		{ID: "prompt.error_prefix", Default: "Sorry, your reply was invalid: "},
		{ID: "prompt.select_instructions", Default: "Use arrows to move, type to filter"},
		{ID: "prompt.input_instructions", Default: "Use arrows to move, enter to select, type to continue"},
		{ID: "prompt.more_help", Default: "for more help"},
		{ID: "prompt.help", Default: "for help"},
		{ID: "prompt.suggestions", Default: "for suggestions"},
	} {
		tmpl = strings.ReplaceAll(tmpl, message.Default, r.text(message.ID, message.Default))
	}
	return tmpl
}

func (r *Renderer) invalidAnswer(value string) string {
	t := r.translator
	if t == nil {
		t = corei18n.EnglishTranslator()
	}
	return t.Translate(corei18n.Message{ID: "prompt.invalid_answer", Default: "{{.Value}} is not a valid answer, please try again.", Args: map[string]any{"Value": fmt.Sprintf("%q", value)}})
}

func AskOne(ctx context.Context, p survey.Prompt, response any, opts ...survey.AskOpt) error {
	if localized, ok := p.(interface{ setTranslator(corei18n.Translator) }); ok {
		localized.setTranslator(corei18n.FromContext(ctx))
	}
	return survey.AskOne(p, response, opts...)
}

func Required(value any) error {
	if err := survey.Required(value); err != nil {
		return diagnostic.Describe(err, corei18n.Message{ID: "prompt.required", Default: "Value is required"})
	}
	return nil
}
