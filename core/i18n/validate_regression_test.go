package i18n

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestResourceValidationCatchesCatalogAndTemplateDefects(t *testing.T) {
	for _, tc := range []struct{ name, en, zh, want string }{
		{"duplicate", `{"m":{"other":"x"},"m":{"other":"y"}}`, `{"m":{"other":"中"}}`, "duplicate"},
		{"syntax", `{"m":{"other":"{{if .Name}}x"}}`, `{"m":{"other":"中"}}`, "invalid template"},
		{"trailing", `{"m":{"other":"x"}} {}`, `{"m":{"other":"中"}}`, "trailing data"},
		{"plural parameters", `{"m":{"one":"{{.Count}} x","other":"x"}}`, `{"m":{"other":"中"}}`, "inconsistent named parameters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fstest.MapFS{"m.en.json": &fstest.MapFile{Data: []byte(tc.en)}, "m.zh.json": &fstest.MapFile{Data: []byte(tc.zh)}}
			require.ErrorContains(t, ValidateResources(files), tc.want)
		})
	}
	files := fstest.MapFS{
		"m.en.json": &fstest.MapFile{Data: []byte(`{"m":{"other":"{{if .Name}}{{.Name}} {{.Name}}{{else}}{{.Count | printf \"%d\"}}{{end}}"}}`)},
		"m.zh.json": &fstest.MapFile{Data: []byte(`{"m":{"other":"{{.Count}} {{.Name}}"}}`)},
	}
	require.NoError(t, ValidateResources(files))
}

func TestMissingArgumentsNeverEmitPartialTemplateOutput(t *testing.T) {
	zh, err := NewTranslator(Chinese)
	require.NoError(t, err)
	require.Equal(t, "errors.config.invalid_range", zh.Translate(Message{ID: "errors.config.invalid_range", Args: map[string]any{"Start": 1}}))
}
