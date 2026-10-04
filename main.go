package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	surveyterm "github.com/AlecAivazis/survey/v2/terminal"
	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/spf13/viper"
	"go.etcd.io/bbolt"

	"github.com/iyear/tdl/cmd"
	"github.com/iyear/tdl/pkg/consts"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := cmd.New().ExecuteContext(ctx); err != nil {
		printError(color.Error, err, viper.GetBool(consts.FlagDebug))
		os.Exit(1)
	}
}

func printError(w io.Writer, err error, debug bool) {
	humanizeErrors := map[error]string{
		bbolt.ErrTimeout:        "Current database is used by another process, please terminate it first",
		surveyterm.InterruptErr: "Interrupted",
	}

	message := err.Error()
	for e, m := range humanizeErrors {
		if errors.Is(err, e) {
			message = m
			break
		}
	}
	_, _ = color.New(color.FgRed, color.Bold).Fprint(w, "Error: ")
	_, _ = fmt.Fprintln(w, message)
	if debug {
		_, _ = fmt.Fprintf(w, "\nDebug details:\n%+v\n", err)
	}
}
