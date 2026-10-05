package autodl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExportStreamPublishesCompleteWindow(t *testing.T) {
	dir := t.TempDir()
	s, err := newExportStream(dir, 123)
	require.NoError(t, err)
	defer s.Abort()
	require.NoFileExists(t, s.path)
	items := []exportMessage{{ID: 1, Type: "message", Text: "说明 \"quoted\"\n正文"}, {ID: 2, File: "media.mp4"}}
	for _, item := range items {
		require.NoError(t, s.Add(item))
	}
	require.NoError(t, s.Finalize())
	data, err := os.ReadFile(s.path)
	require.NoError(t, err)
	var window exportFile
	require.NoError(t, json.Unmarshal(data, &window))
	require.Equal(t, int64(123), window.ID)
	require.Equal(t, items, window.Messages)
	entries, err := os.ReadDir(filepath.Join(dir, tmpDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestExportStreamFailureDoesNotPublishPartialJSON(t *testing.T) {
	s, err := newExportStream(t.TempDir(), 1)
	require.NoError(t, err)
	require.NoError(t, s.Add(exportMessage{ID: 1}))
	require.NoError(t, s.file.Close())
	require.Error(t, s.Finalize())
	s.Abort()
	require.NoFileExists(t, s.path)
	require.NoFileExists(t, s.file.Name())
}
