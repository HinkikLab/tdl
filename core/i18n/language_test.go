package i18n

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeLanguageUsesSimplifiedChineseForAllChineseTags(t *testing.T) {
	for _, value := range []string{"zh", "zh-CN", "zh-TW", "zh-Hant-TW", "ZH_HK.UTF-8"} {
		lang, err := NormalizeLanguage(value)
		require.NoError(t, err, value)
		require.Equal(t, Chinese, lang, value)
	}
	lang, err := NormalizeLanguage("en-US")
	require.NoError(t, err)
	require.Equal(t, English, lang)
	_, err = NormalizeLanguage("fr-FR")
	require.Error(t, err)
}

func TestResolveLanguagePriority(t *testing.T) {
	getenv := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	for _, tc := range []struct {
		name, explicit, system string
		env                    map[string]string
		want                   Language
	}{
		{name: "explicit beats env", explicit: "en", system: "zh-CN", env: map[string]string{"TDL_LANGUAGE": "zh"}, want: English},
		{name: "TDL_LANGUAGE beats locale", system: "en-US", env: map[string]string{"TDL_LANGUAGE": "zh-Hant", "LC_ALL": "en_US"}, want: Chinese},
		{name: "locale beats system", system: "en-US", env: map[string]string{"LANG": "zh_TW.UTF-8"}, want: Chinese},
		{name: "system fallback", system: "zh-HK", want: Chinese},
		{name: "English fallback", system: "fr-FR", env: map[string]string{"LANG": "de_DE"}, want: English},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lang, err := ResolveLanguage(tc.explicit, getenv(tc.env), func() string { return tc.system })
			require.NoError(t, err)
			require.Equal(t, tc.want, lang)
		})
	}
}
