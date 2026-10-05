package i18n

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	corei18n "github.com/iyear/tdl/core/i18n"
)

func TestResolveCommandLanguageRespectsFlagValuesSeparatorsAndExtensions(t *testing.T) {
	root := &cobra.Command{Use: "tdl"}
	root.PersistentFlags().String("language", "auto", "")
	root.PersistentFlags().String("proxy", "", "")
	download := &cobra.Command{Use: "download"}
	download.Flags().String("url", "", "")
	root.AddCommand(download)
	ext := &cobra.Command{Use: "local-ext", DisableFlagParsing: true}
	root.AddCommand(ext)
	getenv := func(key string) string {
		if key == "TDL_LANGUAGE" {
			return "en"
		}
		return ""
	}
	locale := func() string { return "zh-CN" }

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "global explicit", args: []string{"--language", "zh", "download"}, want: "zh"},
		{name: "equals explicit", args: []string{"--language=zh", "download"}, want: "zh"},
		{name: "empty explicit", args: []string{"--language="}, want: "error"},
		{name: "value that looks like a flag", args: []string{"--proxy", "--language", "zh", "download"}, want: "en"},
		{name: "subcommand flag consumes language token", args: []string{"download", "--url", "--language", "zh"}, want: "en"},
		{name: "end marker", args: []string{"--language", "en", "--", "--language", "zh"}, want: "en"},
		{name: "extension flags stay opaque", args: []string{"local-ext", "--language", "zh"}, want: "en"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lang, err := ResolveCommandLanguage(root, tc.args, getenv, locale)
			if tc.want == "error" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, string(lang))
		})
	}
}

func TestApplicationCatalogsLoadAndValidate(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		selected, err := corei18n.NormalizeLanguage(lang)
		require.NoError(t, err)
		translator, err := New(selected)
		require.NoError(t, err)
		require.Equal(t, selected, translator.Language())
	}
}

func TestLocalizedCobraText(t *testing.T) {
	translator, err := New(corei18n.Chinese)
	require.NoError(t, err)

	root := &cobra.Command{Use: "tdl"}
	root.PersistentFlags().String("size", "", "part size")
	require.NoError(t, root.PersistentFlags().MarkDeprecated("size", "part size has been set to maximum by default"))
	BindCommandTree(root, translator)
	require.Equal(t, "分片大小现在默认采用最大值；此参数将在未来版本移除。", root.PersistentFlags().Lookup("size").Annotations[deprecatedAnnotation][0])
}
