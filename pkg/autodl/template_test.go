package autodl

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTemplate = `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`

func TestNameTemplateParse(t *testing.T) {
	tpl, err := newNameTemplate(testTemplate)
	require.NoError(t, err)
	assert.NotNil(t, tpl)
}

func TestNameTemplateExecute(t *testing.T) {
	tpl, err := newNameTemplate(testTemplate)
	require.NoError(t, err)

	got, err := tpl.execute(&fileTemplate{
		DialogID:  1006503122,
		MessageID: 546,
		FileName:  "tglwal.mp4",
	})
	require.NoError(t, err)
	assert.Equal(t, "1006503122_546_tglwal.mp4", got)
}

func TestNameTemplateInvalid(t *testing.T) {
	_, err := newNameTemplate("{{ .DialogID ")
	assert.Error(t, err)
}
