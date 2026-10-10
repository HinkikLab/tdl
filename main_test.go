package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"

	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/autodl"
	"github.com/iyear/tdl/pkg/console"
)

// printError renders errors exactly as main does for an English run.
func printError(w io.Writer, err error, debug bool) {
	console.PrintError(w, err, corei18n.EnglishTranslator(), debug)
}

func TestPrintErrorSeparatesConfigurationContextAndDebugStack(t *testing.T) {
	err := &autodl.ConfigError{
		Path: "config.json", Job: 1,
		ChatURL: "https://t.me/c/2255983776/41872/", Err: errors.New("tags must not contain blanks"),
	}
	var output bytes.Buffer
	printError(&output, err, false)
	require.Contains(t, output.String(), "config \"config.json\" is invalid\n  Job: 1\n  Chat: https://t.me/c/2255983776/41872/\n  Reason: tags must not contain blanks\n")
	require.NotContains(t, output.String(), "main_test.go")
	require.NotContains(t, output.String(), "github.com/")
	output.Reset()
	printError(&output, err, true)
	parts := strings.Split(output.String(), "\nDebug details:\n")
	require.Len(t, parts, 2)
	require.Contains(t, parts[0], "Reason: tags must not contain blanks")
	require.NotContains(t, parts[0], "main_test.go")
	require.Contains(t, parts[1], "main_test.go")
}

func TestPrintErrorOnlyColorsHeadingAndKeepsHumanizedCauses(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	old := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = old })
	var output bytes.Buffer
	printError(&output, errors.Wrap(bbolt.ErrTimeout, "open storage"), false)
	require.Contains(t, output.String(), "\x1b[")
	parts := strings.SplitN(output.String(), "\x1b[0m", 2)
	require.Len(t, parts, 2)
	require.NotContains(t, parts[1], "\x1b[")
	require.Contains(t, parts[1], "Current database is used by another process")
}
