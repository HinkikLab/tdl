package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestLinkedCompletionIncludesResolutionSelection(t *testing.T) {
	root := t.TempDir()
	data := []byte("data")
	opts := LinkedOptions{Threads: 1, Limit: 1}
	require.NoError(t, opts.Links.Normalize())
	link, err := parseResourceLink("https://t.me/resources/10")
	require.NoError(t, err)
	post := tagPost{ChatID: 1, MessageID: 42, Directory: "Caption [42]"}
	dir := filepath.Join(root, post.Directory)
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.bin"), data, 0o644))
	saved := linkedPost{tagPost: post, Version: 1, LinkHash: linkedFingerprint([]resourceLink{link}, opts), Complete: true, Resources: []archivedResource{{Identity: "document_98", File: "old.bin", Size: int64(len(data))}}}
	require.NoError(t, writeArchiveMetadata(dir, saved, nil))
	timing := opts
	timing.Links.BotTimeout++
	timing.Links.BotRequestInterval = 30
	require.Equal(t, linkedFingerprint([]resourceLink{link}, opts), linkedFingerprint([]resourceLink{link}, timing), "retry/timing changes preserve selection identity")
	opts.Links.MaxDepth++
	require.Nil(t, completedLinkedPost(dir, post, linkedFingerprint([]resourceLink{link}, opts)))
	requests := 0
	resolver := &linkResolver{opts: opts.Links, backend: linkedFakeBackend{fetch: func(context.Context, resourceLink) ([]resourceMessage, error) {
		requests++
		return []resourceMessage{{Peer: &tg.InputPeerUser{UserID: 100}, Message: linkedDocument(10, 99, "ref", data)}}, nil
	}}}
	opts.Pool = linkedPool{tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.UploadGetFileRequest); !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
	}))}
	require.NoError(t, archiveLinkedPost(t.Context(), root, post, nil, nil, []resourceLink{link}, resolver, opts))
	require.Equal(t, 1, requests, "expanded resolution cannot reuse shallower completion")
	require.FileExists(t, filepath.Join(dir, "document_99_notes.bin"))
}
