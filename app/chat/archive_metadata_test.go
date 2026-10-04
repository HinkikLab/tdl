package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArchiveMetadataOutput(t *testing.T) {
	for _, mode := range []string{"default", "enabled", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			var enabled *bool
			if mode != "default" {
				value := mode == "enabled"
				enabled = &value
			}
			post := tagPost{ChatID: 1, MessageID: 42, Text: "Original caption #tag", Messages: []tagMedia{{ID: 42, File: "photo.jpg"}}}
			require.NoError(t, writeArchiveMetadata(dir, post, enabled))
			require.NoFileExists(t, filepath.Join(dir, "message.txt"))
			require.NoFileExists(t, filepath.Join(dir, "message.json"))
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			if mode == "disabled" {
				require.Empty(t, entries)
				return
			}
			require.Len(t, entries, 1)
			require.Equal(t, "meta.json", entries[0].Name())
			post.Text = "Updated caption #tag"
			require.NoError(t, writeArchiveMetadata(dir, post, enabled))
			b, err := readArchiveMetadata(dir)
			require.NoError(t, err)
			var saved tagPost
			require.NoError(t, json.Unmarshal(b, &saved))
			require.Equal(t, post, saved)
			entries, err = os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "metadata replacement must leave no temporary files")
			disabled := false
			require.NoError(t, writeArchiveMetadata(dir, tagPost{}, &disabled))
			after, err := readArchiveMetadata(dir)
			require.NoError(t, err)
			require.Equal(t, b, after, "disabling output preserves existing metadata")
		})
	}
}

func TestArchiveDirectoryMigrationWithMetadata(t *testing.T) {
	for _, name := range []string{"message.json", "meta.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			oldDir := filepath.Join(root, "message_42")
			target := filepath.Join(root, "tag Caption [42]")
			require.NoError(t, os.Mkdir(oldDir, 0o755))
			require.NoError(t, writeArchiveJSON(filepath.Join(oldDir, name), tagPost{MessageID: 42}))
			require.NoError(t, os.WriteFile(filepath.Join(oldDir, "photo.jpg"), []byte("photo"), 0o644))
			require.NoError(t, migrateTagDirectory(root, target, 42))
			require.NoDirExists(t, oldDir)
			b, err := os.ReadFile(filepath.Join(target, "photo.jpg"))
			require.NoError(t, err)
			require.Equal(t, "photo", string(b))
		})
	}
}

func TestArchiveMetadataPrefersCurrentFile(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "message_42")
	target := filepath.Join(root, "tag Caption [42]")
	require.NoError(t, os.Mkdir(oldDir, 0o755))
	require.NoError(t, writeArchiveJSON(filepath.Join(oldDir, "message.json"), tagPost{MessageID: 42}))
	require.NoError(t, writeArchiveJSON(filepath.Join(oldDir, "meta.json"), tagPost{MessageID: 99}))
	require.NoError(t, migrateTagDirectory(root, target, 42))
	require.DirExists(t, oldDir)
	require.NoDirExists(t, target, "stale legacy metadata must not override meta.json")
}
