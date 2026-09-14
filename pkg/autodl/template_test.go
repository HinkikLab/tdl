package autodl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTemplate = `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`

func TestNameTemplateIDsOnly(t *testing.T) {
	// the default template depends on the file name, so it is not predictable
	tpl, err := newNameTemplate(testTemplate)
	require.NoError(t, err)
	assert.False(t, tpl.idsOnly())

	tpl, err = newNameTemplate(`{{ .DialogID }}_{{ .MessageID }}.bin`)
	require.NoError(t, err)
	assert.True(t, tpl.idsOnly())
	assert.Equal(t, "1006503122_546.bin", tpl.name(1006503122, 546))

	tpl, err = newNameTemplate(`static.bin`)
	require.NoError(t, err)
	assert.False(t, tpl.idsOnly(), "a literal template does not depend on the ids")
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

func TestExistingNames(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.mp4"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "b.mp4"), []byte("x"), 0o644))

	names := existingNames(dir)
	assert.Contains(t, names, "a.mp4")
	assert.Contains(t, names, "b.mp4")

	// a missing directory is not an error
	assert.Empty(t, existingNames(filepath.Join(dir, "nope")))
}
