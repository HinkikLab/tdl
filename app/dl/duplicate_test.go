package dl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/pkg/tmessage"
)

func TestDuplicateSourcesScheduleOnlyOneFile(t *testing.T) {
	data := []byte("payload")
	var downloads atomic.Int32
	m := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 123}}
	m.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 42, Size: int64(len(data)), DCID: 2, MimeType: "video/mp4"}})
	api := tg.NewClient(referenceRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		var reply bin.Encoder
		switch in.(type) {
		case *tg.ChannelsGetChannelsRequest:
			reply = &tg.MessagesChats{Chats: []tg.ChatClass{&tg.Channel{ID: 123, Title: "Source", Photo: &tg.ChatPhotoEmpty{}}}}
		case *tg.MessagesGetHistoryRequest:
			reply = &tg.MessagesMessages{Messages: []tg.MessageClass{m}}
		case *tg.UploadGetFileRequest:
			downloads.Add(1)
			reply = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
		var encoded bin.Buffer
		if err := reply.Encode(&encoded); err != nil {
			return err
		}
		return out.Decode(&encoded)
	}))
	pool := referencePool{api}
	peer := &tg.InputPeerChannel{ChannelID: 123}
	opts := Options{Dir: t.TempDir(), Template: "file.bin"}
	i, err := newIter(pool, peers.Options{}.Build(api), [][]*tmessage.Dialog{{{Peer: peer, Messages: []int{7}}, {Peer: peer, Messages: []int{7}}}}, opts, 0)
	require.NoError(t, err)
	p := newProgress(pw.NewWriter(), i, opts)
	require.NoError(t, downloader.New(downloader.Options{Pool: pool, Threads: 1, Iter: i, Progress: p, SkipParts: true}).Download(t.Context(), 2))
	require.NoError(t, i.Drain())
	require.EqualValues(t, 1, downloads.Load())
	require.Len(t, i.Completed(), 1)
	got, err := os.ReadFile(filepath.Join(opts.Dir, "file.bin"))
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.NoFileExists(t, filepath.Join(opts.Dir, "file.bin.tmp"))
}
