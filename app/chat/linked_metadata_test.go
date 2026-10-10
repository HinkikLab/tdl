package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestLinkedArchiveWithoutMetadataStillSkipsCompletedFiles(t *testing.T) {
	root := t.TempDir()
	data := []byte("resource contents")
	enabled := false
	opts := LinkedOptions{Threads: 1, Limit: 1, WriteMetadata: &enabled}
	require.NoError(t, opts.Links.Normalize())
	link, err := parseResourceLink("https://t.me/resources/10")
	require.NoError(t, err)
	requests := 0
	resolver := &linkResolver{opts: opts.Links, backend: linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
		requests++
		return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(10, 99, "ref", data)}}, nil
	}}}
	var downloads atomic.Int32
	opts.Pool = linkedPool{tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.UploadGetFileRequest); !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		downloads.Add(1)
		return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
	}))}
	post := tagPost{ChatID: 1, MessageID: 42, Directory: "Caption [42]", Text: "Original caption"}
	dir := filepath.Join(root, post.Directory)
	run := func() {
		require.NoError(t, archiveLinkedPost(context.Background(), root, post, nil, &tg.InputPeerChannel{ChannelID: 1}, []resourceLink{link}, resolver, opts))
	}
	run()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "disabled metadata leaves only the downloaded media")
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	require.Equal(t, data, b)
	require.EqualValues(t, 1, downloads.Load())
	run()
	require.Equal(t, 2, requests, "without metadata the chain is resolved again")
	require.EqualValues(t, 1, downloads.Load(), "exact-size files must still skip downloading")
	require.NoFileExists(t, filepath.Join(dir, "meta.json"))
	enabled = true
	run()
	require.Equal(t, 3, requests)
	require.EqualValues(t, 1, downloads.Load())
	require.FileExists(t, filepath.Join(dir, "meta.json"))
	run()
	require.Equal(t, 3, requests, "re-enabling metadata restores completion checks before resolution")
	require.NoFileExists(t, filepath.Join(dir, "message.json"))
	require.NoFileExists(t, filepath.Join(dir, "message.txt"))
}

func TestLinkedArchiveLegacyCompletion(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			root := t.TempDir()
			post := tagPost{ChatID: 1, MessageID: 42, Directory: "Caption [42]", Text: "Original caption"}
			dir := filepath.Join(root, post.Directory)
			opts := LinkedOptions{WriteMetadata: &enabled}
			require.NoError(t, opts.Links.Normalize())
			link, err := parseResourceLink("https://t.me/resources/10")
			require.NoError(t, err)
			require.NoError(t, os.Mkdir(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "file.zip"), []byte("data"), 0o644))
			saved := linkedPost{
				tagPost: post, Version: 1, LinkHash: linkedFingerprint([]resourceLink{link}, opts), Complete: true,
				Resources: []archivedResource{{File: "file.zip", Size: 4}},
			}
			require.NoError(t, writeArchiveJSON(filepath.Join(dir, "message.json"), saved))
			// A nil resolver and pool ensure a completed legacy archive never
			// requests the chain or downloads its media again.
			require.NoError(t, archiveLinkedPost(context.Background(), root, post, nil, nil, []resourceLink{link}, nil, opts))
			if enabled {
				require.FileExists(t, filepath.Join(dir, "meta.json"))
			} else {
				require.NoFileExists(t, filepath.Join(dir, "meta.json"))
			}
			require.FileExists(t, filepath.Join(dir, "message.json"), "existing archive files are preserved")
		})
	}
}

func TestLinkedArchiveResolutionFailureMetadata(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			root := t.TempDir()
			post := tagPost{ChatID: 1, MessageID: 42, Directory: "Caption [42]"}
			opts := LinkedOptions{WriteMetadata: &enabled}
			require.NoError(t, opts.Links.Normalize())
			link, err := parseResourceLink("https://t.me/resources/10")
			require.NoError(t, err)
			resolver := &linkResolver{opts: opts.Links, backend: linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
				return nil, fmt.Errorf("resource unavailable")
			}}}
			require.ErrorContains(t, archiveLinkedPost(context.Background(), root, post, nil, nil, []resourceLink{link}, resolver, opts), "resource unavailable")
			dir := filepath.Join(root, post.Directory)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			if enabled {
				require.Len(t, entries, 1)
				require.Equal(t, "meta.json", entries[0].Name())
			} else {
				require.Empty(t, entries, "the failure path must also honor disabled metadata")
			}
		})
	}
}
