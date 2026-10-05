package autodl

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	appi18n "github.com/iyear/tdl/pkg/i18n"
)

func TestExampleLanguagesAndMultilineCommentsPreserveConfiguration(t *testing.T) {
	var reference any
	for _, lang := range []corei18n.Language{corei18n.English, corei18n.Chinese} {
		translator, err := appi18n.New(lang)
		require.NoError(t, err)
		text, err := renderExampleConfig(translator)
		require.NoError(t, err)
		var decoded any
		require.NoError(t, yaml.Unmarshal([]byte(text), &decoded))
		if reference == nil {
			reference = decoded
		} else {
			require.Equal(t, reference, decoded)
		}
	}
	text, err := renderExampleConfig(multilineCommentTranslator{})
	require.NoError(t, err)
	var decoded any
	require.NoError(t, yaml.Unmarshal([]byte(text), &decoded))
	require.Equal(t, reference, decoded)
}

type multilineCommentTranslator struct{}

func (multilineCommentTranslator) Language() corei18n.Language { return corei18n.English }
func (multilineCommentTranslator) Translate(corei18n.Message) string {
	return "description\r\nthreads: 0\ninvalid: [yaml"
}

func TestOversizeArchiveRangeHasSafetyLimitDiagnostic(t *testing.T) {
	start, end := 1, MaxRangeMessages+2
	job := Job{StartComment: &start, EndComment: &end}
	err := job.validateArchiveRange(false)
	require.Error(t, err)
	zh, loadErr := appi18n.New(corei18n.Chinese)
	require.NoError(t, loadErr)
	text := diagnostic.FormatError(err, zh)
	require.Contains(t, text, "安全")
	require.NotContains(t, text, "必须小于")
}
