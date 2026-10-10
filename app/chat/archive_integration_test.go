package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/storage"
)

type archiveMemory struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *archiveMemory) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (s *archiveMemory) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *archiveMemory) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func archiveChannel(id int64) *tg.Channel {
	c := &tg.Channel{ID: id, Title: "Archive source", Photo: &tg.ChatPhotoEmpty{}}
	c.SetAccessHash(7)
	return c
}

func TestTagArchiveDownloadsWholeBoundaryAlbumAndKeepsTruncatedWindow(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		t.Run(fmt.Sprint(incremental), func(t *testing.T) {
			data := []byte("video bytes")
			media := map[int]*tg.Message{}
			for id := 8; id <= 12; id++ {
				m := linkedDocument(id, int64(100+id), "ref", data)
				m.PeerID = &tg.PeerChannel{ChannelID: 50}
				m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).MimeType = "video/mp4"
				m.Message = "#notes caption"
				if id >= 9 && id <= 11 {
					m.SetGroupedID(77)
				}
				media[id] = m
			}
			pages, downloads := 0, 0
			api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.ContactsResolveUsernameRequest:
					return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: 50}, Chats: []tg.ChatClass{archiveChannel(50)}}, out)
				case *tg.ChannelsGetChannelsRequest:
					return linkedReply(&tg.MessagesChats{Chats: []tg.ChatClass{archiveChannel(50)}}, out)
				case *tg.MessagesGetHistoryRequest:
					if req.Limit == 1 {
						m := media[req.OffsetID-1]
						require.NotNil(t, m)
						return linkedReply(&tg.MessagesMessages{Messages: []tg.MessageClass{m}}, out)
					}
					pages++
					var batch []tg.MessageClass
					if pages == 1 {
						for id := 12; id >= 8; id-- {
							batch = append(batch, media[id])
						}
					}
					return linkedReply(&tg.MessagesMessagesSlice{Count: 5, Messages: batch}, out)
				case *tg.ChannelsGetMessagesRequest:
					var batch []tg.MessageClass
					for _, id := range req.ID {
						batch = append(batch, media[id.(*tg.InputMessageID).ID])
					}
					return linkedReply(&tg.MessagesChannelMessages{Messages: batch}, out)
				case *tg.UploadGetFileRequest:
					downloads++
					return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
				default:
					return fmt.Errorf("unexpected RPC %T", in)
				}
			}))
			opts := TagOptions{
				Chat: "https://t.me/source", Tag: "#notes", Dir: t.TempDir(), Pool: linkedPool{api}, Threads: 1, Limit: 1,
				Window: ArchiveWindow{StartID: 10, EndID: 11},
			}
			if incremental {
				opts.Window = ArchiveWindow{Until: 1700000000}
				opts.MaxPosts = 1
			}
			err := downloadTag(context.Background(), api, nil, &archiveMemory{}, opts)
			if incremental {
				require.ErrorContains(t, err, "keeping last_ts")
				require.Equal(t, 1, downloads)
			} else {
				require.NoError(t, err)
				require.Equal(t, 3, downloads)
				b, err := os.ReadFile(filepath.Join(opts.Dir, "50", "notes caption [9]", "meta.json"))
				require.NoError(t, err)
				var post tagPost
				require.NoError(t, json.Unmarshal(b, &post))
				require.Equal(t, []int{9, 10, 11}, []int{post.Messages[0].ID, post.Messages[1].ID, post.Messages[2].ID})
			}
		})
	}
}

func TestLinkedArchiveSkipsDeadTargetsAndStillDownloadsHealthyPosts(t *testing.T) {
	data := []byte("actual resource bytes")
	resolveCalls := map[string]int{}
	pages, downloads := 0, 0
	api := tg.NewClient(linkedRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch req := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			resolveCalls[strings.ToLower(req.Username)]++
			if strings.EqualFold(req.Username, "dead_bot") || req.Username == "old_group" {
				return tgerr.New(400, "USERNAME_NOT_OCCUPIED")
			}
			id := int64(50)
			if req.Username == "healthy" {
				id = 60
			}
			return linkedReply(&tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: id}, Chats: []tg.ChatClass{archiveChannel(id)}}, out)
		case *tg.MessagesGetHistoryRequest:
			pages++
			var batch []tg.MessageClass
			if pages == 1 {
				for i, link := range []string{
					"https://t.me/dead_bot?start=a", "tg://resolve?domain=DEAD_bot&start=b",
					"https://t.me/old_group/1", "https://t.me/old_group/2", "https://t.me/healthy/10",
				} {
					batch = append(batch, &tg.Message{ID: 100 - i, Date: 1700000000, PeerID: &tg.PeerChannel{ChannelID: 50}, Message: "Source caption " + link})
				}
			}
			return linkedReply(&tg.MessagesMessagesSlice{Count: 5, Messages: batch}, out)
		case *tg.ChannelsGetMessagesRequest:
			m := linkedDocument(10, 99, "ref", data)
			m.PeerID = &tg.PeerChannel{ChannelID: 60}
			return linkedReply(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{m}}, out)
		case *tg.UploadGetFileRequest:
			downloads++
			return linkedReply(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}, out)
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
	}))
	comments := false
	opts := LinkedOptions{
		Chat: "https://t.me/source", Dir: t.TempDir(), Threads: 1, Limit: 1, Pool: linkedPool{api},
		Window: ArchiveWindow{Since: 1700000000, Until: 1700000000}, Links: LinkOptions{ScanComments: &comments},
	}
	require.NoError(t, downloadLinked(context.Background(), api, &archiveMemory{}, opts))
	require.Equal(t, 1, resolveCalls["dead_bot"])
	require.Equal(t, 1, resolveCalls["old_group"])
	require.Equal(t, 1, downloads)
	entries, err := os.ReadDir(filepath.Join(opts.Dir, "50"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "dead posts must not produce empty archives")
	b, err := os.ReadFile(filepath.Join(opts.Dir, "50", entries[0].Name(), "document_99_notes.bin"))
	require.NoError(t, err)
	require.Equal(t, data, b)
	pages = 0
	opts.MaxPosts = 1
	require.ErrorContains(t, downloadLinked(context.Background(), api, &archiveMemory{}, opts), "keeping last_ts")
	require.Equal(t, 1, downloads, "reruns validate the completed healthy archive")
	require.Equal(t, 2, resolveCalls["dead_bot"], "a new run probes restored targets again")
	pages = 0
	opts.CheckOnly = true
	opts.Dir = filepath.Join(t.TempDir(), "preview")
	require.NoError(t, downloadLinked(context.Background(), api, &archiveMemory{}, opts), "a bounded check-only preview is successful")
	require.NoDirExists(t, opts.Dir)
	require.Equal(t, 1, downloads)
	require.Equal(t, 2, resolveCalls["dead_bot"], "check-only must not request bots")
}
