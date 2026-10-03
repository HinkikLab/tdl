package cmd

import (
	"github.com/spf13/cobra"

	"github.com/iyear/tdl/pkg/autodl"
)

const batchInitAnnotation = "tdl:batch-config-init"

// NewBatchInit creates examples locally, independently of Telegram sessions.
func NewBatchInit() *cobra.Command {
	var path string
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate an annotated config.json with examples of every batch mode",
		Long: `Generate a valid JSON configuration containing direct message ranges,
comment ranges, incremental chat/topic/reply jobs, caption tag archives and
linked-resource archives. Keep the jobs you need and replace the example URLs,
IDs and tags before running batch. Explanations use _comment; the comment field
selects comment mode. No Telegram connection or login is required.

Existing configurations are preserved unless --force is specified.`,
		Example:     "  tdl batch init\n  tdl batch init -c examples/config.json\n  tdl batch init -c config.json --force",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{batchInitAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := autodl.WriteExampleConfig(path, force); err != nil {
				return err
			}
			cmd.Printf("Generated %s (11 example jobs).\n", path)
			cmd.Println("Keep the jobs you need, replace example URLs/IDs/tags and select your namespace.")
			cmd.Printf("Then run: tdl batch -c %q --check-only\n", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&path, "config", "c", autodl.DefaultConfigFile, "path for the generated JSON config")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing config file")
	_ = cmd.MarkFlagFilename("config", "json")
	return cmd
}
