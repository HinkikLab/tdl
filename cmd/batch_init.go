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

Comment language is detected from LC_ALL, LC_MESSAGES, LANGUAGE or LANG, then
the Windows UI language, with English as the fallback. Use --lang zh or --lang en
to select a language explicitly. Every option includes an inline explanation.

Existing configurations are preserved unless --force is specified.`,
		Example:     "  tdl batch init\n  tdl batch init --lang zh\n  tdl batch init --lang en -c examples/config.yaml\n  tdl batch init -c config.yaml --force",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{batchInitAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			selected, err := autodl.ResolveExampleLanguage(language)
			if err != nil {
				return err
			}
			if err := autodl.WriteExampleConfigLanguage(path, force, selected); err != nil {
				return err
			}
			if selected == "zh" {
				cmd.Printf("已生成 %s（11 个示例任务，中文注释）。\n", path)
				cmd.Println("保留需要的任务，替换示例链接、消息 ID 和标签，并选择账号命名空间。")
				cmd.Printf("然后运行：tdl batch -c %q --check-only\n", path)
			} else {
				cmd.Printf("Generated %s (11 example jobs, English comments).\n", path)
				cmd.Println("Keep the jobs you need, replace example URLs/IDs/tags and select your namespace.")
				cmd.Printf("Then run: tdl batch -c %q --check-only\n", path)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&path, "config", "c", autodl.DefaultConfigFile, "path for the generated YAML config")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing config file")
	cmd.Flags().StringVar(&language, "lang", "auto", "comment language: auto, zh or en")
	_ = cmd.MarkFlagFilename("config", "yaml", "yml")
	return cmd
}
