package cmd

import (
	"bytes"
	_ "embed"
	"runtime"
	"text/template"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/consts"
)

//go:embed version.tmpl
var version string

func NewVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Check the version info",
		RunE: func(cmd *cobra.Command, args []string) error {
			buf := &bytes.Buffer{}
			if err := template.Must(template.New("version").Parse(version)).Execute(buf, map[string]interface{}{
				"VersionLabel": console.Translate(cmd.Context(), corei18n.Message{ID: "version.version", Default: "Version"}),
				"Version":      consts.Version,
				"CommitLabel":  console.Translate(cmd.Context(), corei18n.Message{ID: "version.commit", Default: "Commit"}),
				"Commit":       consts.Commit,
				"DateLabel":    console.Translate(cmd.Context(), corei18n.Message{ID: "version.date", Default: "Date"}),
				"Date":         consts.CommitDate,
				"GoVersion":    runtime.Version(),
				"GOOS":         runtime.GOOS,
				"GOARCH":       runtime.GOARCH,
			}); err != nil {
				return err
			}
			_, err := color.New(color.FgBlue).Fprint(cmd.OutOrStdout(), buf.String())
			return err
		},
	}
}
