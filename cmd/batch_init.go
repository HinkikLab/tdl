package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/autodl"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
)

const batchInitAnnotation = "tdl:batch-config-init"

// NewBatchInit creates examples locally, independently of Telegram sessions.
func NewBatchInit() *cobra.Command {
	var path string
	var force bool
	var language string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate an annotated config.yaml with examples of every batch mode",
		Long: `Generate a valid YAML configuration containing direct message ranges,
comment ranges, incremental chat/topic/reply jobs, caption tag archives and
linked-resource archives. Keep the jobs you need and replace the example URLs,
IDs and tags before running batch. Explanations are YAML comments; the comment
field selects comment mode. Legacy JSON configs are still accepted. No Telegram
connection or login is required.

Comment language follows the global --language setting. Use --lang zh or --lang en
to override the language used for comments in the generated file. Every option
includes an inline explanation.

Existing configurations are preserved unless --force is specified.`,
		Example:     "  tdl batch init\n  tdl batch init --lang zh\n  tdl batch init --lang en -c examples/config.yaml\n  tdl batch init -c config.yaml --force",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{batchInitAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			selected := ""
			if !strings.EqualFold(strings.TrimSpace(language), "auto") && strings.TrimSpace(language) != "" {
				var err error
				selected, err = autodl.ResolveExampleLanguage(language)
				if err != nil {
					return err
				}
			} else if translator := corei18n.FromContext(cmd.Context()); translator != nil {
				selected = string(translator.Language())
			} else {
				var err error
				selected, err = autodl.ResolveExampleLanguage("auto")
				if err != nil {
					return err
				}
			}
			if err := autodl.WriteExampleConfigLanguage(path, force, selected); err != nil {
				return err
			}
			translator := corei18n.FromContext(cmd.Context())
			languageName := "English"
			if selected == "zh" {
				languageName = "Chinese"
			}
			if translator := corei18n.FromContext(cmd.Context()); translator != nil && translator.Language() == corei18n.Chinese {
				languageName = "英文"
				if selected == "zh" {
					languageName = "中文"
				}
			}
			console.Info(cmd.OutOrStdout(), translator, messages.BatchInitGenerated(path, 11, languageName))
			console.Info(cmd.OutOrStdout(), translator, messages.BatchInitNext(path))
			return nil
		},
	}
	cmd.Flags().StringVarP(&path, "config", "c", autodl.DefaultConfigFile, "path for the generated YAML config")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing config file")
	cmd.Flags().StringVar(&language, "lang", "auto", "comment language: auto, zh or en")
	_ = cmd.MarkFlagFilename("config", "yaml", "yml")
	return cmd
}
