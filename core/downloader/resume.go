package downloader

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/sync/errgroup"
)

type ignoreSchema struct {
	sch *tg.Client
	loc tg.InputFileLocationClass
}

func (s *ignoreSchema) chunk(ctx context.Context, offset int64, limit int) ([]byte, error) {
	req := &tg.UploadGetFileRequest{Offset: offset, Limit: limit, Location: s.loc}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.sch.UploadGetFile(ctx, req)
		if err != nil {
			flood, _ := tgerr.FloodWait(ctx, err)
			if flood {
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if tgerr.Is(err, tg.ErrTimeout) {
				// Avoid a busy retry loop when the server immediately times out.
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
				continue
			}
			return nil, errors.Wrap(err, "get next chunk")
		}
		switch f := res.(type) {
		case *tg.UploadFile:
			return f.Bytes, nil
		default:
			return nil, errors.Errorf("unexpected type %T", res)
		}
	}
}

// parallelIgnore uses a bounded worker group. The first failure cancels every
// worker, and all writes settle before the caller may close the destination.
func (d *Downloader) parallelIgnore(ctx context.Context, client *tg.Client, elem Elem, skip map[int]struct{}, threads int) error {
	size := elem.File().Size()
	total := PartsCount(size)
	sch := &ignoreSchema{sch: client, loc: elem.File().Location()}
	w := newWriteAt(elem, d.opts.Progress, resumer(d.opts.Progress), MaxPartSize)
	resumed := int64(0)
	for index := range skip {
		if index >= 0 && index < total {
			resumed += min(int64(MaxPartSize), size-int64(index)*MaxPartSize)
		}
	}
	w.downloaded.Store(resumed)
	w.resumed = resumed
	if d.opts.Progress != nil {
		d.opts.Progress.OnDownload(elem, ProgressState{
			Downloaded: resumed, Total: size, Resumed: resumed, Skipped: resumed,
		})
	}
	var mu sync.Mutex
	cursor := 0
	take := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		for cursor < total {
			index := cursor
			cursor++
			if _, ok := skip[index]; !ok {
				return index, true
			}
		}
		return 0, false
	}
	g, ctx := errgroup.WithContext(ctx)
	for n := 0; n < min(max(1, threads), total); n++ {
		g.Go(func() error {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				index, ok := take()
				if !ok {
					return nil
				}
				offset := int64(index) * MaxPartSize
				// Telegram requires an aligned limit even for a short final part.
				data, err := sch.chunk(ctx, offset, MaxPartSize)
				if err != nil {
					return err
				}
				if int64(len(data)) != min(int64(MaxPartSize), size-offset) {
					return errors.Wrapf(io.ErrUnexpectedEOF, "part %d: got %d bytes", index, len(data))
				}
				if _, err := w.WriteAt(data, offset); err != nil {
					return errors.Wrap(err, "write output")
				}
			}
		})
	}
	return g.Wait()
}
