package fsutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinWithin(t *testing.T) {
	dir := t.TempDir()
	got, err := JoinWithin(dir, filepath.Join("nested", "file.bin"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "nested", "file.bin"), got)

	for _, name := range []string{"", "..", filepath.Join("..", "outside.bin"), filepath.Join(dir, "absolute.bin")} {
		_, err = JoinWithin(dir, name)
		require.Error(t, err, name)
	}
}
