package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/internal/transfer"
)

func TestTagCompletionValidatesRemoteIdentityAndLocalFileWithoutMetadata(t *testing.T) {
	payload := []byte("original bytes")
	documentID := int64(99)
	reference := "ref"
	pages, downloads := 0, 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 50}, Chats: []tg.ChatClass{archiveChannel(50)}}, out)
		case *tg.MessagesGetHistoryRequest:
			pages++
			var messages []tg.MessageClass
			if pages == 1 {
				m := linkedDocument(7, documentID, reference, payload)
				m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).MimeType = "video/mp4"
				m.Message = "#notes caption"
				messages = []tg.MessageClass{m}
			}
			return linkedReply(&tg.MessagesMessagesSlice{Count: 1, Messages: messages}, out)
		case *tg.UploadGetFileRequest:
			downloads++
			return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: payload}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	off := false
	opts := TagOptions{Chat: "https://t.me/source", Tag: "#notes", Dir: t.TempDir(), Pool: linkedPool{api}, Threads: 1, Limit: 1, WriteMetadata: &off, Account: "account:user:1"}
	dir := filepath.Join(opts.Dir, "50", "notes caption [7]")
	path := filepath.Join(dir, "7_notes.bin")
	run := func(want int) {
		pages = 0
		require.NoError(t, downloadTag(t.Context(), api, nil, &archiveMemory{}, opts))
		require.Equal(t, want, downloads)
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, payload, got)
	}
	run(1)
	run(1)
	reference = "fresh"
	run(1)
	documentID++
	payload = []byte("replaced bytes")
	run(2)
	old, err := os.ReadFile(path + ".unverified")
	require.NoError(t, err)
	require.Equal(t, []byte("original bytes"), old)
	require.NoError(t, os.WriteFile(path, []byte("modified bytes"), 0o644))
	require.NoError(t, os.Chtimes(path, time.Unix(1700000010, 0), time.Unix(1700000010, 0)))
	run(3)
	require.NoError(t, os.Remove(path))
	run(4)
	require.NoError(t, os.Truncate(path, 1))
	run(5)
	require.NoFileExists(t, filepath.Join(dir, "meta.json"))
	stateBytes, err := os.ReadFile(filepath.Join(dir, tagCompletedName))
	require.NoError(t, err)
	require.NotContains(t, string(stateBytes), "caption")
	var state tagCompletedState
	require.NoError(t, json.Unmarshal(stateBytes, &state))
	require.Equal(t, documentID, state.Files[7].Identity.ID)
	require.Equal(t, "channel:50", state.Source)
	require.Equal(t, opts.Account, state.Account)
	before, err := os.Stat(filepath.Join(dir, tagCompletedName))
	require.NoError(t, err)
	run(5)
	after, err := os.Stat(filepath.Join(dir, tagCompletedName))
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime(), "clean reruns do not rewrite completion state")
}

func TestTagLegacyFileWithoutVerifiedIdentityIsPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "7_notes.bin")
	require.NoError(t, os.WriteFile(path, []byte("legacy data"), 0o644))
	require.NoError(t, preserveArchiveFile(path, &transfer.Reservations{}))
	require.NoFileExists(t, path)
	got, err := os.ReadFile(path + ".unverified")
	require.NoError(t, err)
	require.Equal(t, []byte("legacy data"), got)
}
