package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/internal/transfer"
)

func TestTagStreamDownloadsBeforeNextPageAndDoesNotRefetchMessages(t *testing.T) {
	data := []byte("discovered bytes")
	pages, downloads, metadataRequests := 0, 0, 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 50}, Chats: []tg.ChatClass{archiveChannel(50)}}, out)
		case *tg.MessagesGetHistoryRequest:
			if req.Limit == 1 {
				metadataRequests++
				return fmt.Errorf("tag stream must use already discovered media")
			}
			pages++
			var batch []tg.MessageClass
			switch pages {
			case 1:
				for id := 200; id > 100; id-- {
					m := &tg.Message{ID: id, Date: 1700000000, PeerID: &tg.PeerChannel{ChannelID: 50}}
					if id == 200 {
						m = linkedDocument(id, 99, "ref", data)
						m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).MimeType = "video/mp4"
						m.Message = "#notes caption"
					}
					batch = append(batch, m)
				}
			case 2:
				require.Equal(t, 1, downloads, "first selected post must commit before requesting the next page")
			}
			return linkedReply(&tg.MessagesMessagesSlice{Count: 100, Messages: batch}, out)
		case *tg.ChannelsGetMessagesRequest:
			metadataRequests++
			return fmt.Errorf("unexpected per-message metadata request")
		case *tg.UploadGetFileRequest:
			downloads++
			return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	off := false
	var result transfer.Counts
	opts := TagOptions{Chat: "https://t.me/source", Tag: "#notes", Dir: t.TempDir(), Pool: linkedPool{api}, Threads: 1, Limit: 1, WriteMetadata: &off}
	opts.OnResult = func(counts transfer.Counts) { result = counts }
	require.NoError(t, downloadTag(t.Context(), api, nil, &archiveMemory{}, opts))
	require.Equal(t, 1, downloads)
	require.Zero(t, metadataRequests)
	require.EqualValues(t, 1, result.FilesDownloaded)
	require.EqualValues(t, len(data), result.Bytes)
	path := filepath.Join(opts.Dir, "50", "notes caption [200]", "200_notes.bin")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.NoFileExists(t, filepath.Join(filepath.Dir(path), "meta.json"))
	indexes, err := filepath.Glob(filepath.Join(opts.Dir, "50", "index_*.json"))
	require.NoError(t, err)
	require.Len(t, indexes, 1)
	b, err := os.ReadFile(indexes[0])
	require.NoError(t, err)
	var index struct {
		Messages []Message `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(b, &index))
	require.Len(t, index.Messages, 1)
	pages = 0
	require.NoError(t, downloadTag(t.Context(), api, nil, &archiveMemory{}, opts))
	require.Equal(t, 1, downloads, "metadata-disabled rerun still checks existing files")
	require.EqualValues(t, 1, result.FilesExisting)
	require.Zero(t, result.FilesDownloaded)
}

func TestTagCommitFailureReturnsErrorAndKeepsPartial(t *testing.T) {
	data := []byte("video bytes")
	root := t.TempDir()
	target := filepath.Join(root, "50", "notes caption [7]", "7_notes.bin")
	pages := 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 50}, Chats: []tg.ChatClass{archiveChannel(50)}}, out)
		case *tg.MessagesGetHistoryRequest:
			pages++
			var messages []tg.MessageClass
			if pages == 1 {
				m := linkedDocument(7, 99, "ref", data)
				m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).MimeType = "video/mp4"
				m.Message = "#notes caption"
				messages = []tg.MessageClass{m}
			}
			return linkedReply(&tg.MessagesMessagesSlice{Count: 1, Messages: messages}, out)
		case *tg.UploadGetFileRequest:
			require.NoError(t, os.Mkdir(target, 0o755))
			return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	opts := TagOptions{Chat: "https://t.me/source", Tag: "#notes", Dir: root, Pool: linkedPool{api}, Threads: 1, Limit: 1, Window: ArchiveWindow{Until: 1700000000}}
	var result transfer.Counts
	opts.OnResult = func(counts transfer.Counts) { result = counts }
	err := downloadTag(t.Context(), api, nil, &archiveMemory{}, opts)
	require.ErrorContains(t, err, "commit downloaded file")
	require.EqualValues(t, 1, result.FilesFailed)
	require.Zero(t, result.FilesDownloaded)
	require.FileExists(t, target+".tmp")
	require.FileExists(t, target+".tmp.parts")
	require.NoFileExists(t, filepath.Join(filepath.Dir(target), "meta.json"), "failed files never commit post metadata")
	indexes, err := filepath.Glob(filepath.Join(root, "50", "index_*.json"))
	require.NoError(t, err)
	require.Empty(t, indexes)
}
