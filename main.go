package main

import (
	"context"
	"io"
	"os"
	"os/signal"

	"github.com/fatih/color"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/cmd"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/consts"
	pki18n "github.com/iyear/tdl/pkg/i18n"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	root := cmd.NewWithTranslator(nil)
	language, err := pki18n.ResolveCommandLanguage(root, os.Args[1:], os.Getenv, corei18n.SystemLocale)
	if err != nil {
		language, _ = corei18n.ResolveLanguage("", os.Getenv, corei18n.SystemLocale)
		translator, _ := pki18n.New(language)
		pki18n.BindCommandTree(root, translator)
		console.PrintError(color.Error, err, translator, false)
		os.Exit(2)
	}
	translator, err := pki18n.New(language)
	if err != nil {
		console.PrintError(color.Error, err, corei18n.EnglishTranslator(), false)
		os.Exit(1)
	}
	pki18n.BindCommandTree(root, translator)
	ctx = corei18n.WithTranslator(ctx, translator)
	root.SetContext(ctx)
	if err := root.ExecuteContext(ctx); err != nil {
		console.PrintError(color.Error, err, translator, viper.GetBool(consts.FlagDebug))
		os.Exit(1)
	}
}

// printError keeps the stable English helper used by package-level callers.
func printError(w io.Writer, err error, debug bool) {
	console.PrintError(w, err, corei18n.EnglishTranslator(), debug)
}
