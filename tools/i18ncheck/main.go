package main

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/iyear/tdl/cmd"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/autodl"
	pki18n "github.com/iyear/tdl/pkg/i18n"
)

func main() {
	appResources, err := pki18n.ApplicationResources()
	if err != nil {
		fail(err)
	}
	resources := []fs.FS{corei18n.CoreResources(), appResources}
	if err = corei18n.ValidateResources(resources...); err != nil {
		fail(err)
	}
	if err = pki18n.ValidateMessageSources(".", resources...); err != nil {
		fail(err)
	}
	for _, name := range []string{"en", "zh"} {
		language, err := corei18n.NormalizeLanguage(name)
		if err != nil {
			fail(err)
		}
		translator, err := pki18n.New(language)
		if err != nil {
			fail(err)
		}
		for _, id := range autodl.ExampleConfigCommentIDs() {
			if translator.Translate(corei18n.Message{ID: id}) == id {
				fail(fmt.Errorf("missing %s translation for example config comment %q", name, id))
			}
		}
	}
	if err = pki18n.ValidateCommandCoverage(cmd.NewWithTranslator(nil), resources...); err != nil {
		fail(err)
	}
	unlocalized, err := pki18n.FindUnlocalizedOutput(".")
	if err != nil {
		fail(err)
	}
	for _, finding := range unlocalized {
		fmt.Fprintf(os.Stderr, "%s:%d: unlocalized terminal text: %q\n", finding.File, finding.Line, finding.Text)
	}
	if len(unlocalized) > 0 {
		os.Exit(1)
	}
	fmt.Println("Translation resources and Cobra help coverage are valid.")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
