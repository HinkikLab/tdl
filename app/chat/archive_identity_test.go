package chat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArchiveMigrationRequiresChatAndCompatibleSourceAccount(t *testing.T) {
	expected := tagPost{ChatID: 50, MessageID: 42, Source: "channel:50", Account: "account-a"}
	for _, test := range []struct {
		name    string
		saved   tagPost
		migrate bool
	}{
		{"legacy correct chat", tagPost{ChatID: 50, MessageID: 42}, true},
		{"missing chat", tagPost{MessageID: 42}, false},
		{"other chat", tagPost{ChatID: 60, MessageID: 42}, false},
		{"other peer kind", tagPost{ChatID: 50, MessageID: 42, Source: "chat:50"}, false},
		{"other account", tagPost{ChatID: 50, MessageID: 42, Account: "account-b"}, false},
		{"verified owner", expected, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			old := filepath.Join(root, "message_42")
			target := filepath.Join(root, "caption [42]")
			require.NoError(t, os.Mkdir(old, 0o755))
			require.NoError(t, writeArchiveMetadata(old, test.saved, nil))
			require.NoError(t, os.WriteFile(filepath.Join(old, "payload.bin"), []byte("original"), 0o644))
			require.NoError(t, migrateTagDirectory(root, target, expected))
			if test.migrate {
				require.NoDirExists(t, old)
				require.FileExists(t, filepath.Join(target, "payload.bin"))
			} else {
				require.FileExists(t, filepath.Join(old, "payload.bin"))
				require.NoDirExists(t, target)
			}
		})
	}
}

func TestArchiveExistingDirectoryRejectsForeignOwner(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "caption [42]")
	require.NoError(t, os.Mkdir(target, 0o755))
	saved := tagPost{ChatID: 60, MessageID: 42, Source: "channel:60", Account: "account-b"}
	require.NoError(t, writeArchiveMetadata(target, saved, nil))
	expected := tagPost{ChatID: 50, MessageID: 42, Source: "channel:50", Account: "account-a"}
	require.ErrorContains(t, migrateTagDirectory(root, target, expected), "another source, account or post")
	actual, err := readArchiveMetadata(target)
	require.NoError(t, err)
	require.Contains(t, string(actual), "channel:60")
}
