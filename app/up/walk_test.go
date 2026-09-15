package up

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWalkFindsThumbnailWithoutTrimmingBaseName(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "song.png")
	thumb := filepath.Join(dir, "song.thumb")
	require.NoError(t, os.WriteFile(media, []byte("media"), 0o600))
	require.NoError(t, os.WriteFile(thumb, []byte("thumb"), 0o600))

	files, err := walk([]string{media}, nil, nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, thumb, files[0].Thumb)
}

func TestWalkDeduplicatesOverlappingInputsAndNormalizesExtensions(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "VIDEO.MP4")
	require.NoError(t, os.WriteFile(media, []byte("media"), 0o600))

	files, err := walk([]string{dir, media}, []string{"mp4"}, nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, media, files[0].File)
}
