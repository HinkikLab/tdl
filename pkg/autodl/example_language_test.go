package autodl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectExampleLanguage(t *testing.T) {
	for _, tc := range []struct {
		name, system, want string
		env                map[string]string
	}{
		{"Chinese Linux locale", "", "zh", map[string]string{"LANG": "zh_CN.UTF-8"}},
		{"Chinese script locale", "", "zh", map[string]string{"LANG": "zh-Hant-TW"}},
		{"locale modifier", "", "zh", map[string]string{"LANG": "zh_TW@variant"}},
		{"LC_ALL wins", "zh-CN", "en", map[string]string{"LC_ALL": "C.UTF-8", "LC_MESSAGES": "zh_CN", "LANGUAGE": "zh", "LANG": "zh_CN"}},
		{"LC_MESSAGES wins", "en-US", "zh", map[string]string{"LC_MESSAGES": "zh_CN", "LANGUAGE": "en", "LANG": "en_US"}},
		{"language list", "", "zh", map[string]string{"LANGUAGE": "de:zh_CN:en", "LANG": "en_US"}},
		{"first supported language", "", "en", map[string]string{"LANGUAGE": "en:zh_CN"}},
		{"environment wins over Windows", "zh-CN", "en", map[string]string{"LANG": "en_US.UTF-8"}},
		{"unsupported environment", "zh-CN", "en", map[string]string{"LANG": "de_DE"}},
		{"Windows Chinese", "zh-CN", "zh", nil},
		{"Windows English", "en-US", "en", nil},
		{"unknown system", "", "en", nil},
		{"empty environment", "zh-CN", "zh", map[string]string{"LC_ALL": "  "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detectExampleLanguage(func(key string) string { return tc.env[key] }, func() string { return tc.system })
			require.Equal(t, tc.want, got)
		})
	}
}

func TestWriteExampleConfigSelectsLanguage(t *testing.T) {
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	for _, tc := range []struct{ language, marker string }{
		{"auto", "# 01. 直接下载"},
		{"en", "# 01. Direct channel"},
		{"zh", "# 01. 直接下载"},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, WriteExampleConfigLanguage(path, false, tc.language))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(data), tc.marker)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, WriteExampleConfig(path, false))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "# 01. 直接下载")
	require.ErrorContains(t, WriteExampleConfigLanguage(path, true, "invalid"), "use auto, zh or en")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, after, "invalid language must not replace existing configs")
}
