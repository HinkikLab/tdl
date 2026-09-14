package autodl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

type batchRPC func(context.Context, bin.Encoder, bin.Decoder) error

func (f batchRPC) Invoke(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
	return f(ctx, in, out)
}

type batchPool struct{ api *tg.Client }

func (p batchPool) Client(context.Context, int) *tg.Client  { return p.api }
func (p batchPool) Takeout(context.Context, int) *tg.Client { return p.api }
func (p batchPool) Default(context.Context) *tg.Client      { return p.api }
func (p batchPool) Close() error                            { return nil }
func encodeResult(in bin.Encoder, out bin.Decoder) error {
	var b bin.Buffer
	if err := in.Encode(&b); err != nil {
		return err
	}
	return out.Decode(&b)
}

type elemIter struct {
	el   downloader.Elem
	used bool
}

func (i *elemIter) Next(context.Context) bool {
	if i.used {
		return false
	}
	i.used = true
	return true
}
func (i *elemIter) Value() downloader.Elem { return i.el }
func (i *elemIter) Err() error             { return nil }

type signalingProgress struct {
	*jobProgress
	written chan struct{}
	once    sync.Once
}

func (p *signalingProgress) PartDone(e downloader.Elem, i int) {
	p.jobProgress.PartDone(e, i)
	p.once.Do(func() { close(p.written) })
}

func TestBatchDownloadFailureAndResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media.bin")
	data := bytes.Repeat([]byte{0xAB}, 2*downloader.MaxPartSize+13)
	store, state, err := LoadStateStore(filepath.Join(dir, "state.json"))
	require.NoError(t, err)
	newProgress := func() *jobProgress {
		return newJobProgress(pw.NewWriter(), &jobContext{state: state, store: store, logger: zap.NewNop()})
	}
	progress := &signalingProgress{jobProgress: newProgress(), written: make(chan struct{})}
	first := newTestElem(path, int64(len(data)))
	require.NoError(t, first.start(false))
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		req := in.(*tg.UploadGetFileRequest)
		if req.Offset > 0 {
			select {
			case <-progress.written:
			case <-ctx.Done():
				return ctx.Err()
			}
			return errors.New("connection interrupted")
		}
		return encodeResult(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data[:downloader.MaxPartSize]}, out)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, downloader.New(downloader.Options{Pool: batchPool{api}, Threads: 1, Iter: &elemIter{el: first}, Progress: progress, SkipParts: true}).Download(ctx, 1))
	require.False(t, state.IsFinished(2))
	require.NoFileExists(t, path)
	done, failed, _ := progress.Stats()
	require.Zero(t, done)
	require.Equal(t, 1, failed)
	require.FileExists(t, path+tempExt)
	require.Equal(t, map[int]struct{}{0: {}}, downloader.NewPartsStore(path+tempExt, int64(len(data))).Done())

	second := newTestElem(path, int64(len(data)))
	p := newProgress()
	require.NoError(t, second.start(false))
	var mu sync.Mutex
	var offsets []int64
	api = tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		req := in.(*tg.UploadGetFileRequest)
		if req.Limit != downloader.MaxPartSize {
			return errors.New("invalid limit")
		}
		mu.Lock()
		offsets = append(offsets, req.Offset)
		mu.Unlock()
		end := min(int64(len(data)), req.Offset+int64(req.Limit))
		return encodeResult(&tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data[req.Offset:end]}, out)
	}))
	require.NoError(t, downloader.New(downloader.Options{Pool: batchPool{api}, Threads: 4, Iter: &elemIter{el: second}, Progress: p, SkipParts: true}).Download(ctx, 1))
	require.ElementsMatch(t, []int64{downloader.MaxPartSize, 2 * downloader.MaxPartSize}, offsets)
	require.True(t, state.IsFinished(2))
	require.NoFileExists(t, path+tempExt)
	require.NoFileExists(t, downloader.PartsPath(path+tempExt))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.NoError(t, store.Save())
	loaded, err := LoadState(store.path)
	require.NoError(t, err)
	require.True(t, loaded.IsFinished(2))
}

func TestIterBatchesMetadataAndOpensFilesOnDemand(t *testing.T) {
	var requests int
	api := tg.NewClient(batchRPC(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.ChannelsGetMessagesRequest)
		if !ok {
			return fmt.Errorf("unexpected RPC %T", in)
		}
		requests++
		if len(req.ID) > 100 {
			return errors.New("too many message IDs")
		}
		messages := make([]tg.MessageClass, 0, len(req.ID))
		for _, input := range req.ID {
			id := input.(*tg.InputMessageID).ID
			msg := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 123}}
			msg.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: int64(id), Size: 10, DCID: 2, MimeType: "application/octet-stream", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "file.bin"}}}})
			messages = append(messages, msg)
		}
		return encodeResult(&tg.MessagesChannelMessages{Messages: messages}, out)
	}))
	manager := peers.Options{}.Build(api)
	dialog := manager.Channel(&tg.Channel{ID: 123, AccessHash: 42})
	ids := make([]int, 205)
	for i := range ids {
		ids[i] = i + 1
	}
	ids = append(ids, 1, 2)
	dir := t.TempDir()
	it, err := newIter(batchPool{api}, manager, dialog, dir, ids, &iterOptions{template: `{{.MessageID}}.bin`, batch: 1000})
	require.NoError(t, err)
	defer it.Drain()
	var got []int
	for it.Next(context.Background()) {
		e := it.Value().(*elem)
		if len(got) == 0 {
			require.Equal(t, 1, requests)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "only the current file should be open, not the whole metadata batch")
		}
		got = append(got, e.msgID)
		e.cleanupFile()
	}
	require.NoError(t, it.Err())
	require.Len(t, got, 205)
	require.Equal(t, 3, requests)
	for i, id := range got {
		require.Equal(t, i+1, id)
	}
}

func TestIncompleteIncrementalWindowNeverAdvancesWithYes(t *testing.T) {
	store, state, err := LoadStateStore(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, err)
	state.SetLastTS(100)
	state.Finish(1)
	r := &Runner{opts: Options{Yes: true}}
	require.Error(t, r.advanceIncremental(context.Background(), &Job{}, store, state, []int{1, 2}, 200))
	require.Equal(t, int64(100), state.GetLastTS())
}

func TestFinishedTargetsNeedNoPeerLookup(t *testing.T) {
	state := NewState()
	state.Finish(1, 2)
	r := &Runner{}
	require.Empty(t, r.missing(context.Background(), &Job{}, Link{}, t.TempDir(), []int{1, 2}, state, nil))
}

func TestIterDoesNotSkipTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1.bin")
	require.NoError(t, os.WriteFile(path, []byte("short"), 0600))
	dialog := (&peers.Manager{}).Channel(&tg.Channel{ID: 123})
	it, err := newIter(nil, nil, dialog, dir, nil, &iterOptions{template: `{{.MessageID}}.bin`})
	require.NoError(t, err)
	defer it.Drain()
	msg := &tg.Message{ID: 1}
	msg.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 1, Size: 10, DCID: 2, MimeType: "application/octet-stream"}})
	it.pending = []*tg.Message{msg}
	require.True(t, it.Next(context.Background()))
	e := it.Value().(*elem)
	require.Equal(t, 1, e.msgID)
	require.False(t, it.isFinished(1))
	e.cleanupFile()
}
