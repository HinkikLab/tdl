package dl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/tmedia"
)

type referenceRPC func(context.Context, bin.Encoder, bin.Decoder) error

func (f referenceRPC) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	return f(ctx, in, out)
}

type referencePool struct{ api *tg.Client }

func (p referencePool) Client(context.Context, int) *tg.Client  { return p.api }
func (p referencePool) Takeout(context.Context, int) *tg.Client { return p.api }
func (p referencePool) Default(context.Context) *tg.Client      { return p.api }
func (p referencePool) Close() error                            { return nil }

type referenceIter struct {
	elem *iterElem
	used bool
}

func (i *referenceIter) Next(context.Context) bool {
	if i.used {
		return false
	}
	i.used = true
	return true
}
func (i *referenceIter) Value() downloader.Elem { return i.elem }
func (i *referenceIter) Err() error             { return nil }

func TestRegularDownloadRefreshesExpiredFileReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.bin")
	data := []byte("complete media")
	f, parts, err := downloader.OpenPartial(path+tempExt, int64(len(data)))
	require.NoError(t, err)
	defer f.Close()
	msg := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 123}}
	msg.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 42, Size: int64(len(data)), DCID: 2, FileReference: []byte("old"), MimeType: "application/octet-stream"}})
	media, ok := tmedia.GetMedia(msg)
	require.True(t, ok)
	e := &iterElem{id: 1, logicalPos: 0, from: (&peers.Manager{}).Channel(&tg.Channel{ID: 123}), fromMsg: msg, file: media, to: f, parts: parts}
	refreshes := 0
	api := tg.NewClient(referenceRPC(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		var reply bin.Encoder
		switch req := in.(type) {
		case *tg.UploadGetFileRequest:
			if string(req.Location.(*tg.InputDocumentFileLocation).FileReference) == "old" {
				return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
			}
			reply = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}
		case *tg.ChannelsGetMessagesRequest:
			refreshes++
			fresh := *msg
			fresh.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 42, Size: int64(len(data)), DCID: 2, FileReference: []byte("fresh"), MimeType: "application/octet-stream"}})
			reply = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{&fresh}}
		default:
			return fmt.Errorf("unexpected RPC %T", in)
		}
		var b bin.Buffer
		if err := reply.Encode(&b); err != nil {
			return err
		}
		return out.Decode(&b)
	}))
	finished := &iter{mu: &sync.Mutex{}, finished: make(map[int]struct{})}
	p := newProgress(pw.NewWriter(), finished, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, downloader.New(downloader.Options{Pool: referencePool{api}, Threads: 1, Iter: &referenceIter{elem: e}, Progress: p, SkipParts: true}).Download(ctx, 1))
	require.Equal(t, 1, refreshes)
	require.Equal(t, map[int]struct{}{0: {}}, finished.Finished())
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.NoFileExists(t, path+tempExt)
	require.NoFileExists(t, downloader.PartsPath(path+tempExt))
}
