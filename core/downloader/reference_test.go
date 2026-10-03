package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/require"
)

type sourceElem struct {
	*testElem
	peer tg.InputPeerClass
}

type reissuedElem struct {
	*testElem
	refresh func(context.Context) (File, error)
}

func (e *reissuedElem) RefreshFile(ctx context.Context, _ tg.InputFileLocationClass) (File, error) {
	return e.refresh(ctx)
}

func (e *sourceElem) FileSource() (tg.InputPeerClass, int) { return e.peer, 7 }

type referenceProgress struct {
	callbackProgress
	skip map[int]struct{}
}

func (p referenceProgress) Resume(Elem) (map[int]struct{}, int64, bool) {
	return p.skip, int64(len(p.skip) * MaxPartSize), len(p.skip) > 0
}

func referenceResult(in bin.Encoder, out bin.Decoder) error {
	var b bin.Buffer
	if err := in.Encode(&b); err != nil {
		return err
	}
	return out.Decode(&b)
}

func referenceMessage(ref []byte, size int64) *tg.Message {
	msg := &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 123}}
	msg.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID: 42, AccessHash: 43, FileReference: ref, DCID: 2, Size: size, MimeType: "application/octet-stream",
	}})
	return msg
}

func TestDownloadRefreshesExpiredReferenceWithoutRestart(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%v", resume), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			data := bytes.Repeat([]byte{0xAB}, 2*MaxPartSize+13)
			f, err := os.Create(filepath.Join(t.TempDir(), "out"))
			require.NoError(t, err)
			defer f.Close()
			el := &sourceElem{testElem: &testElem{
				f: f, size: int64(len(data)), loc: &tg.InputDocumentFileLocation{ID: 42, FileReference: []byte("old")},
			}, peer: &tg.InputPeerChannel{ChannelID: 123, AccessHash: 456}}
			p := referenceProgress{}
			if resume {
				_, err = f.WriteAt(data[:MaxPartSize], 0)
				require.NoError(t, err)
				p.skip = map[int]struct{}{0: {}}
			}
			var states []ProgressState
			p.download = func(s ProgressState) { states = append(states, s) }
			var completed error
			p.done = func(_ Elem, err error) { completed = err }
			var offsets []int64
			refreshes := 0
			api := tg.NewClient(rpcFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				switch req := in.(type) {
				case *tg.ChannelsGetMessagesRequest:
					refreshes++
					channel := req.Channel.(*tg.InputChannel)
					if channel.ChannelID != 123 || channel.AccessHash != 456 || req.ID[0].(*tg.InputMessageID).ID != 7 {
						return errors.New("wrong source message")
					}
					return referenceResult(&tg.MessagesChannelMessages{Messages: []tg.MessageClass{referenceMessage([]byte("fresh"), int64(len(data)))}}, out)
				case *tg.UploadGetFileRequest:
					offsets = append(offsets, req.Offset)
					if req.Offset >= MaxPartSize && string(fileReference(req.Location)) == "old" {
						return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
					}
					return (&mockRPC{data: data}).Invoke(ctx, in, out)
				default:
					return fmt.Errorf("unexpected request %T", in)
				}
			}))
			d := New(Options{Pool: testPool{api}, Iter: &singleIter{elem: el}, Progress: p, Threads: 1, SkipParts: true})
			require.NoError(t, d.Download(ctx, 1))
			require.NoError(t, completed)
			require.Equal(t, 1, refreshes)
			count := 0
			for _, off := range offsets {
				if off == 0 {
					count++
				}
			}
			if resume {
				require.Zero(t, count, "completed resume parts must not be fetched")
			} else {
				require.Equal(t, 1, count, "a completed chunk must not be fetched again after expiry")
			}
			require.Equal(t, int64(len(data)), states[len(states)-1].Downloaded)
			require.Equal(t, []byte("old"), fileReference(el.Location()), "do not mutate metadata observed by progress callbacks")
			got, err := os.ReadFile(f.Name())
			require.NoError(t, err)
			require.Equal(t, data, got)
		})
	}
}

func TestReferenceRefreshSharedByConcurrentChunks(t *testing.T) {
	const workers = 8
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var expired atomic.Int32
	var refreshes atomic.Int32
	ready := make(chan struct{})
	r := &referenceInvoker{
		location: &tg.InputDocumentFileLocation{ID: 42, FileReference: []byte("old")},
		fetch: func(context.Context) (tg.InputFileLocationClass, error) {
			refreshes.Add(1)
			return &tg.InputDocumentFileLocation{ID: 42, FileReference: []byte("fresh")}, nil
		},
		next: rpcFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
			req := in.(*tg.UploadGetFileRequest)
			if string(fileReference(req.Location)) == "old" {
				if expired.Add(1) == workers {
					close(ready)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ready:
					return tgerr.New(400, "FILE_REFERENCE_INVALID")
				}
			}
			if req.Limit != MaxPartSize || req.Offset%MaxPartSize != 0 || !req.GetPrecise() {
				return errors.New("chunk parameters changed on retry")
			}
			return referenceResult(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte("ok")}, out)
		}),
	}
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			req := &tg.UploadGetFileRequest{Offset: int64(i * MaxPartSize), Limit: MaxPartSize}
			req.SetPrecise(true)
			_, err := tg.NewClient(r).UploadGetFile(ctx, req)
			results <- err
		}()
	}
	for i := 0; i < workers; i++ {
		require.NoError(t, <-results)
	}
	// A later chunk must use the refreshed reference immediately.
	req := &tg.UploadGetFileRequest{Limit: MaxPartSize}
	req.SetPrecise(true)
	_, err := tg.NewClient(r).UploadGetFile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, int32(workers), expired.Load())
	require.Equal(t, int32(1), refreshes.Load())
}

func TestReferenceRefreshFailuresAreBounded(t *testing.T) {
	for _, mode := range []string{"unchanged", "fetch fails", "always expired", "unrelated error"} {
		t.Run(mode, func(t *testing.T) {
			calls, refreshes := 0, 0
			expected := errors.New("connection interrupted")
			r := &referenceInvoker{
				location: &tg.InputDocumentFileLocation{FileReference: []byte("old")},
				next: rpcFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
					calls++
					if mode == "unrelated error" {
						return expected
					}
					return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
				}),
				fetch: func(context.Context) (tg.InputFileLocationClass, error) {
					refreshes++
					if mode == "fetch fails" {
						return nil, expected
					}
					ref := []byte("old")
					if mode == "always expired" {
						ref = []byte(fmt.Sprint(refreshes))
					}
					return &tg.InputDocumentFileLocation{FileReference: ref}, nil
				},
			}
			_, err := tg.NewClient(r).UploadGetFile(context.Background(), &tg.UploadGetFileRequest{})
			require.Error(t, err)
			switch mode {
			case "unchanged":
				require.ErrorContains(t, err, "same file reference")
			case "fetch fails", "unrelated error":
				require.ErrorIs(t, err, expected)
			case "always expired":
				require.Equal(t, 4, calls)
				require.Equal(t, 3, refreshes)
			}
			if mode == "unrelated error" {
				require.Zero(t, refreshes)
			} else {
				require.True(t, tgerr.Is(err, "FILE_REFERENCE_EXPIRED"), "preserve the original RPC error")
			}
		})
	}
}

func TestReferenceRefreshWaitCanBeCanceled(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &referenceInvoker{location: &tg.InputDocumentFileLocation{}, fetch: func(ctx context.Context) (tg.InputFileLocationClass, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	var wg sync.WaitGroup
	wg.Go(func() { _ = r.refresh(ctx, 0) })
	<-started
	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitCancel()
	require.ErrorIs(t, r.refresh(waitCtx, 0), context.Canceled)
	cancel()
	wg.Wait()
}

func TestRefreshMessageFileValidatesAttachment(t *testing.T) {
	for _, mode := range []string{"document", "photo", "private chat", "deleted", "no media", "wrong message", "changed ID", "changed size", "changed DC"} {
		t.Run(mode, func(t *testing.T) {
			file := &testElem{size: 20, loc: &tg.InputDocumentFileLocation{ID: 42}}
			msg := referenceMessage([]byte("fresh"), 20)
			peer := tg.InputPeerClass(&tg.InputPeerChannel{ChannelID: 123})
			if mode == "private chat" {
				peer = &tg.InputPeerUser{UserID: 123}
			}
			switch mode {
			case "photo":
				file.loc = &tg.InputPhotoFileLocation{ID: 42, ThumbSize: "x"}
				msg.SetMedia(&tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 42, DCID: 2, FileReference: []byte("fresh"), Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", Size: 20}}}})
			case "changed ID":
				file.loc = &tg.InputDocumentFileLocation{ID: 999}
			case "changed size":
				file.size++
			case "changed DC":
				msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).DCID = 3
			case "no media":
				msg = &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 123}}
			case "wrong message":
				msg.ID = 8
			}
			api := tg.NewClient(rpcFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				messages := []tg.MessageClass{msg}
				if mode == "deleted" {
					messages = []tg.MessageClass{&tg.MessageEmpty{ID: 7}}
				}
				if mode == "private chat" {
					if _, ok := in.(*tg.MessagesGetMessagesRequest); !ok {
						return fmt.Errorf("wrong metadata RPC %T", in)
					}
					return referenceResult(&tg.MessagesMessages{Messages: messages}, out)
				}
				return referenceResult(&tg.MessagesChannelMessages{Messages: messages}, out)
			}))
			loc, err := refreshMessageFile(context.Background(), api, peer, 7, file)
			if mode == "document" || mode == "photo" || mode == "private chat" {
				require.NoError(t, err)
				require.Equal(t, []byte("fresh"), fileReference(loc))
			} else {
				require.Error(t, err)
				require.Nil(t, loc)
				if mode == "deleted" || mode == "no media" {
					require.ErrorContains(t, err, "no longer has media")
				} else if mode == "wrong message" {
					require.ErrorContains(t, err, "unavailable or deleted")
				} else {
					require.ErrorContains(t, err, "media changed")
				}
			}
		})
	}
}

func TestReissuedFileRefresherValidatesReplacement(t *testing.T) {
	for _, mode := range []string{"valid", "nil", "changed ID", "changed size", "changed type", "same reference"} {
		t.Run(mode, func(t *testing.T) {
			old := &testElem{size: 20, loc: &tg.InputDocumentFileLocation{ID: 42, FileReference: []byte("old")}}
			fresh := &testElem{size: 20, loc: &tg.InputDocumentFileLocation{ID: 42, FileReference: []byte("fresh")}}
			switch mode {
			case "changed ID":
				fresh.loc.(*tg.InputDocumentFileLocation).ID++
			case "changed size":
				fresh.size++
			case "changed type":
				fresh.loc = &tg.InputPhotoFileLocation{ID: 42}
			case "same reference":
				fresh.loc.(*tg.InputDocumentFileLocation).FileReference = []byte("old")
			}
			elem := &reissuedElem{testElem: old, refresh: func(context.Context) (File, error) {
				if mode == "nil" {
					return nil, nil
				}
				return fresh, nil
			}}
			api := tg.NewClient(rpcFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				req := in.(*tg.UploadGetFileRequest)
				if string(fileReference(req.Location)) == "old" {
					return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
				}
				return referenceResult(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte("ok")}, out)
			}))
			client := New(Options{Pool: testPool{api}}).client(context.Background(), elem)
			_, err := client.UploadGetFile(context.Background(), &tg.UploadGetFileRequest{Location: old.loc, Limit: MaxPartSize})
			if mode == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
