package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
)

func TestLinkedDeletedBotMessageResumesAfterReissue(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"in-flight", "repeated-deletion", "restart-after-reissue-failure", "changed-file", "request-interval"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			data := bytes.Repeat([]byte{0x42, 0x17, 0x93}, downloader.MaxPartSize+43)
			opts := LinkedOptions{Threads: 1, Limit: 1, Links: LinkOptions{BotTimeout: 3, BotIdle: 1, PollInterval: 10}}
			if mode == "request-interval" {
				opts.Links.BotRequestInterval = 2
			}
			require.NoError(t, opts.Links.Normalize())
			link, err := parseResourceLink("https://t.me/files_bot?start=fixture")
			require.NoError(t, err)
			post := tagPost{ChatID: 1, MessageID: 42, Directory: "Caption [42]"}
			path := filepath.Join(root, post.Directory, "document_99_notes.bin")
			starts, deletedLookups := 0, 0
			var requestTimes []time.Time
			firstReplyTime := time.Time{}
			var offsets []int64
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.ContactsResolveUsernameRequest:
					bot := &tg.User{ID: 100, Bot: true}
					bot.SetUsername("files_bot")
					bot.SetAccessHash(55)
					return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 100}, Users: []tg.UserClass{bot}}, out)
				case *tg.MessagesGetHistoryRequest:
					// The bot self-deletes its replies before the next history poll.
					return linkedReply(&tg.MessagesMessages{}, out)
				case *tg.MessagesStartBotRequest:
					starts++
					requestTimes = append(requestTimes, time.Now())
					if mode == "restart-after-reissue-failure" && starts == 2 {
						return fmt.Errorf("simulated bot request interruption")
					}
					ref := "old"
					if starts > 1 {
						ref = fmt.Sprintf("fresh-%d", starts)
					}
					docID := int64(99)
					if mode == "changed-file" && starts > 1 {
						docID++
					}
					base := starts * 10
					message := linkedDocument(base+2, docID, ref, data)
					if mode == "request-interval" {
						message.Date = int(time.Now().Unix())
						if starts == 1 {
							firstReplyTime = time.Unix(int64(message.Date), 0)
						}
					}
					return linkedReply(&tg.Updates{Updates: []tg.UpdateClass{
						&tg.UpdateMessageID{ID: base, RandomID: req.RandomID},
						&tg.UpdateNewMessage{Message: message},
					}}, out)
				case *tg.MessagesGetMessagesRequest:
					deletedLookups++
					id := req.ID[0].(*tg.InputMessageID).ID
					return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageEmpty{ID: id}}}, out)
				case *tg.UploadGetFileRequest:
					offsets = append(offsets, req.Offset)
					ref := string(req.Location.(*tg.InputDocumentFileLocation).FileReference)
					if req.Offset >= downloader.MaxPartSize && ref == "old" ||
						mode == "repeated-deletion" && req.Offset >= 2*downloader.MaxPartSize && ref == "fresh-2" {
						return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
					}
					end := min(int64(len(data)), req.Offset+int64(req.Limit))
					return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data[req.Offset:end]}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			backend := &telegramLinkBackend{api: api, manager: peers.Options{}.Build(api), opts: opts.Links}
			resolver := &linkResolver{opts: opts.Links, backend: backend}
			opts.Pool = linkedPool{api}
			err = archiveLinkedPost(t.Context(), root, post, nil, nil, []resourceLink{link}, resolver, opts)
			if mode == "restart-after-reissue-failure" || mode == "changed-file" {
				if mode == "changed-file" {
					require.ErrorContains(t, err, "resource set changed")
				} else {
					require.ErrorContains(t, err, "simulated bot request interruption")
				}
				require.NoFileExists(t, path)
				require.FileExists(t, downloader.PartsPath(path+".tmp"))
				partial, readErr := os.ReadFile(path + ".tmp")
				require.NoError(t, readErr)
				require.Equal(t, downloader.MaxPartSize, len(partial))
				require.Equal(t, sha256.Sum256(data[:downloader.MaxPartSize]), sha256.Sum256(partial))
				require.Nil(t, completedLinkedPost(filepath.Dir(path), post, linkedFingerprint([]resourceLink{link}, opts)))
				if mode == "restart-after-reissue-failure" {
					// A fresh session reopens the persisted journal, as another run
					// would, after the bot is available again.
					resolver = &linkResolver{opts: opts.Links, backend: &telegramLinkBackend{api: api, manager: peers.Options{}.Build(api), opts: opts.Links}}
					err = archiveLinkedPost(t.Context(), root, post, nil, nil, []resourceLink{link}, resolver, opts)
				} else {
					require.Equal(t, []int64{0, downloader.MaxPartSize}, offsets)
					require.Equal(t, 2, starts)
					return
				}
			}
			require.NoError(t, err)
			if mode == "request-interval" {
				require.False(t, requestTimes[1].Before(firstReplyTime.Add(2*time.Second)), "file reissues also respect the last bot reply")
			}
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, len(data), len(got))
			require.Equal(t, sha256.Sum256(data), sha256.Sum256(got))
			expectedOffsets := []int64{0, downloader.MaxPartSize, downloader.MaxPartSize, 2 * downloader.MaxPartSize, 3 * downloader.MaxPartSize}
			switch mode {
			case "repeated-deletion":
				expectedOffsets = []int64{0, downloader.MaxPartSize, downloader.MaxPartSize, 2 * downloader.MaxPartSize, 2 * downloader.MaxPartSize, 3 * downloader.MaxPartSize}
				require.Equal(t, 3, starts)
				require.Equal(t, 2, deletedLookups)
			case "restart-after-reissue-failure":
				require.Equal(t, 3, starts)
				require.Equal(t, 1, deletedLookups)
			default:
				require.Equal(t, 2, starts)
				require.Equal(t, 1, deletedLookups)
			}
			require.Equal(t, expectedOffsets, offsets, "retry only failed chunks; never fetch the completed first part again")
			require.NoFileExists(t, path+".tmp")
			require.NoFileExists(t, downloader.PartsPath(path+".tmp"))
			metadata, err := os.ReadFile(filepath.Join(filepath.Dir(path), "meta.json"))
			require.NoError(t, err)
			var saved linkedPost
			require.NoError(t, json.Unmarshal(metadata, &saved))
			require.True(t, saved.Complete)
			require.Equal(t, starts*10+2, saved.Resources[0].MessageID, "save the reissued source message ID")
		})
	}
}
