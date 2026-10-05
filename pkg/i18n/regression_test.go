package i18n

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
)

func TestBootstrapShorthandValuesAndBooleanClusters(t *testing.T) {
	root := &cobra.Command{Use: "tdl"}
	root.PersistentFlags().StringP("language", "L", "auto", "")
	root.PersistentFlags().BoolP("yes", "y", false, "")
	root.PersistentFlags().BoolP("continue", "c", false, "")
	root.PersistentFlags().IntP("threads", "t", 8, "")
	root.PersistentFlags().StringP("proxy", "p", "", "")
	for _, tc := range []struct {
		args []string
		want corei18n.Language
	}{
		{[]string{"-yc", "--language=zh"}, corei18n.Chinese},
		{[]string{"-t8", "--language=zh"}, corei18n.Chinese},
		{[]string{"-Lzh"}, corei18n.Chinese},
		{[]string{"-yLen"}, corei18n.English},
		{[]string{"-p", "--language", "zh"}, corei18n.English},
	} {
		lang, err := ResolveCommandLanguage(root, tc.args, func(string) string { return "en" }, nil)
		require.NoError(t, err)
		require.Equal(t, tc.want, lang, "%v", tc.args)
	}
}

func TestHelpDefaultsPreserveNestedUserValuesAndPlainOutput(t *testing.T) {
	zh, err := New(corei18n.Chinese)
	require.NoError(t, err)
	root := &cobra.Command{Use: "tdl"}
	value := `folder (default data) "with quotes"`
	root.PersistentFlags().String("proxy", value, "Proxy address")
	root.AddCommand(&cobra.Command{Use: "probe", Run: func(c *cobra.Command, _ []string) { c.Println(value) }})
	BindCommandTree(root, zh)
	var help, data bytes.Buffer
	root.SetOut(&help)
	require.NoError(t, root.Help())
	require.Contains(t, help.String(), `（默认："folder (default data) \"with quotes\""）`)
	root.SetOut(&data)
	root.SetArgs([]string{"probe"})
	require.NoError(t, root.Execute())
	require.Equal(t, value+"\n", data.String())
}

func TestCompletionUsesRedirectedWriterWithoutRewritingScript(t *testing.T) {
	zh, err := New(corei18n.Chinese)
	require.NoError(t, err)
	root := &cobra.Command{Use: "tdl"}
	root.AddCommand(&cobra.Command{Use: "probe", Run: func(*cobra.Command, []string) {}})
	BindCommandTree(root, zh)
	var expected, actual bytes.Buffer
	require.NoError(t, root.GenBashCompletionV2(&expected, true))
	root.SetOut(&actual)
	root.SetArgs([]string{"completion", "bash"})
	require.NoError(t, root.Execute())
	require.Equal(t, expected.String(), actual.String())
}

func TestRequiredAndExclusiveFlagsFailBeforePreRun(t *testing.T) {
	zh, err := New(corei18n.Chinese)
	require.NoError(t, err)
	for _, required := range []bool{true, false} {
		root := &cobra.Command{Use: "tdl", SilenceErrors: true, SilenceUsage: true}
		preRuns := 0
		child := &cobra.Command{Use: "probe", PreRun: func(*cobra.Command, []string) { preRuns++ }, Run: func(*cobra.Command, []string) {}}
		child.Flags().Bool("first", false, "")
		child.Flags().Bool("second", false, "")
		args := []string{"probe"}
		if required {
			require.NoError(t, child.MarkFlagRequired("first"))
		} else {
			child.MarkFlagsMutuallyExclusive("first", "second")
			args = append(args, "--first", "--second")
		}
		root.AddCommand(child)
		BindCommandTree(root, zh)
		root.SetArgs(args)
		err := root.Execute()
		require.Error(t, err)
		require.Zero(t, preRuns)
		require.Contains(t, diagnostic.FormatError(err, zh), "--first")
		require.NotEqual(t, err.Error(), diagnostic.FormatError(err, zh))
	}
}

func TestUnknownCommandRetainsLegacyValidationAndLocalizedHelp(t *testing.T) {
	zh, err := New(corei18n.Chinese)
	require.NoError(t, err)
	root := &cobra.Command{Use: "tdl", SilenceErrors: true, SilenceUsage: true}
	preRuns := 0
	root.PreRun = func(*cobra.Command, []string) { preRuns++ }
	root.Run = func(*cobra.Command, []string) {}
	root.AddCommand(&cobra.Command{Use: "probe", Run: func(*cobra.Command, []string) {}})
	BindCommandTree(root, zh)
	root.SetArgs([]string{"prob"})
	err = root.Execute()
	require.ErrorContains(t, err, `unknown command "prob"`)
	require.Contains(t, diagnostic.FormatError(err, zh), `没有子命令 "prob"`)
	require.Zero(t, preRuns)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"help", "invalid-topic"})
	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), "未知帮助主题")
}

func TestDeprecatedFlagWarningUsesOnlySelectedLanguage(t *testing.T) {
	zh, err := New(corei18n.Chinese)
	require.NoError(t, err)
	root := &cobra.Command{Use: "tdl", Run: func(*cobra.Command, []string) {}}
	root.PersistentFlags().Int("size", 1, "part size")
	require.NoError(t, root.PersistentFlags().MarkDeprecated("size", "part size has been set to maximum by default"))
	BindCommandTree(root, zh)
	var warnings bytes.Buffer
	root.SetErr(&warnings)
	root.SetArgs([]string{"--size", "2"})
	require.NoError(t, root.Execute())
	require.Contains(t, warnings.String(), "参数 --size 已弃用")
	require.NotContains(t, warnings.String(), "has been deprecated")
	require.NotContains(t, warnings.String(), "cli.deprecated")
}
