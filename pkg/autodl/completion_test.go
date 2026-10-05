package autodl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/downloader"
)

func TestCompletedMediaRevalidatedAcrossRuns(t *testing.T) {
	for _, change := range []string{"unchanged", "missing", "truncated", "same size local edit", "same size remote replacement", "changed DC"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "7.bin")
			store, state, err := LoadStateStore(filepath.Join(dir, "state.json"))
			require.NoError(t, err)
			data := []byte("AAAA")
			docID, dc := int64(42), 2
			metadataCalls, uploads := 0, 0
			api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.ChannelsGetMessagesRequest:
					metadataCalls++
					msg := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 123}}
					msg.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: docID, DCID: dc, Size: int64(len(data)), MimeType: "application/octet-stream"}})
					return encodeResult(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{msg}}, out)
				case *tg.UploadGetFileRequest:
					uploads++
					require.Equal(t, docID, req.Location.(*tg.InputDocumentFileLocation).ID)
					return encodeResult(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			manager := peers.Options{}.Build(api)
			dialog := manager.Channel(&tg.Channel{ID: 123, AccessHash: 456})
			run := func() {
				t.Helper()
				it, err := newIter(batchPool{api}, manager, dialog, dir, nil, &iterOptions{
					state: state, template: "{{.MessageID}}.bin", rangeStart: 7, rangeEnd: 8,
					isFinished: state.IsTerminalWithoutMedia, onFinish: func(ids []int) { state.Finish(ids...) },
				})
				require.NoError(t, err)
				p := newJobProgress(pw.NewWriter(), &jobContext{state: state, store: store, logger: zap.NewNop()})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err = downloader.New(downloader.Options{Pool: batchPool{api}, Threads: 1, Iter: it, Progress: p, SkipParts: true}).Download(ctx, 1)
				it.Drain()
				require.NoError(t, err)
				require.NoError(t, p.Err())
				require.NoError(t, store.Save())
				require.True(t, state.IsFinished(7))
			}
			run()
			require.Equal(t, 1, uploads)
			_, recorded := state.Media(7)
			require.True(t, recorded, "success must retain the committed media identity")
			// Reload persisted records rather than trusting the in-memory state.
			store, state, err = LoadStateStore(store.path)
			require.NoError(t, err)
			switch change {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "truncated":
				require.NoError(t, os.WriteFile(path, []byte("A"), 0600))
			case "same size local edit":
				require.NoError(t, os.WriteFile(path, []byte("EDIT"), 0600))
				// Make the recorded change deterministic on coarse timestamp filesystems.
				future := time.Now().Add(time.Hour)
				require.NoError(t, os.Chtimes(path, future, future))
			case "same size remote replacement":
				docID++
				data = []byte("BBBB")
			case "changed DC":
				dc++
			}
			run()
			require.Equal(t, 2, metadataCalls, "completed IDs still need current media metadata")
			if change == "unchanged" {
				require.Equal(t, 1, uploads, "valid committed bytes should be reused")
			} else {
				require.Equal(t, 2, uploads)
			}
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, data, got)
			if change == "same size remote replacement" || change == "changed DC" || change == "same size local edit" {
				backups, err := filepath.Glob(path + ".unverified*")
				require.NoError(t, err)
				require.Len(t, backups, 1, "preserve incompatible completed output")
			}
		})
	}
}
