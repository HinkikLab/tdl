package downloader

import (
	"context"
	"sync"

	"github.com/go-faster/errors"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// ignoreSchema is the chunk source bound to a concrete file location.
//
// It mirrors the schema used by the gotd downloader so that tdl is able to
// download only the parts that are still missing instead of the whole file.
type ignoreSchema struct {
	sch *tg.Client
	loc tg.InputFileLocationClass
}

func (s *ignoreSchema) chunk(ctx context.Context, offset int64, limit int) ([]byte, error) {
	req := &tg.UploadGetFileRequest{
		Offset:   offset,
		Limit:    limit,
		Location: s.loc,
	}

	for {
		res, err := s.sch.UploadGetFile(ctx, req)
		if err != nil {
			// same policy as the gotd downloader: retry flood wait and
			// timeouts, propagate everything else
			flood, ferr := tgerr.FloodWait(ctx, err)
			if ferr != nil {
				if flood || tgerr.Is(ferr, tg.ErrTimeout) {
					continue
				}
				return nil, errors.Wrap(ferr, "get next chunk")
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

type part struct {
	index  int
	offset int64
	data   []byte
}

// parallelIgnore downloads elem into its destination while reusing the parts
// that are already present on disk.
//
// skip is the set of part indexes that do not need to be fetched again. Their
// bytes still count towards the file size, so the caller must guarantee that
// the previous data is still in place.
func (d *Downloader) parallelIgnore(ctx context.Context, client *tg.Client, elem Elem, skip map[int]struct{}, threads int) error {
	loc, size := elem.File().Location(), elem.File().Size()
	total := partsCount(size)
	if threads <= 0 {
		threads = 1
	}

	sch := &ignoreSchema{
		sch: client,
		loc: loc,
	}

	progress, _ := d.opts.Progress.(Resumer)

	var (
		writers sync.WaitGroup
		sem     = make(chan struct{}, threads)
		mu      sync.Mutex
		cursor  int
		failed  sync.Once
		fetchEr error
	)

	// The shared cursor hands out the next part to fetch, skipping the parts
	// that are already on disk.
	take := func() (int, int64, int, bool) {
		mu.Lock()
		defer mu.Unlock()

		for cursor < total {
			idx := cursor
			cursor++

			if _, ok := skip[idx]; ok {
				continue
			}

			offset := int64(idx) * int64(MaxPartSize)
			limit := int(min(int64(MaxPartSize), size-offset))
			if limit <= 0 {
				continue
			}

			return idx, offset, limit, true
		}

		return 0, 0, 0, false
	}

	// fail records the first failure and stops the fetcher that shares stop
	// with the failed writer.
	fail := func(stop chan struct{}, err error) {
		failed.Do(func() {
			fetchEr = err
			close(stop)
		})
	}

	// fetchOne keeps pulling parts until the work is exhausted or the
	// destination is unavailable. The writer that owns the queue is the one
	// tracked by the group, so a failure here only has to stop that writer.
	fetchOne := func(q chan part, stop chan struct{}) {
		defer close(q)

		for {
			select {
			case <-ctx.Done():
				fail(stop, ctx.Err())
				return
			case <-stop:
				return
			default:
			}

			idx, offset, limit, ok := take()
			if !ok {
				return
			}

			select {
			case <-ctx.Done():
				fail(stop, ctx.Err())
				return
			case <-stop:
				return
			case sem <- struct{}{}:
			}

			data, err := sch.chunk(ctx, offset, limit)
			<-sem
			if err != nil {
				fail(stop, err)
				return
			}

			select {
			case <-ctx.Done():
				fail(stop, ctx.Err())
				return
			case <-stop:
				return
			case q <- part{index: idx, offset: offset, data: data}:
			}
		}
	}

	writers.Add(threads)
	for i := 0; i < threads; i++ {
		// Each writer owns exactly one queue and the matching fetcher is its
		// only producer, so a queue is closed only once its fetcher is done.
		// stop is shared by exactly that pair.
		var (
			q    = make(chan part, 1)
			stop = make(chan struct{})
		)

		go func() { // writer: it owns the destination file
			defer writers.Done()

			for p := range q {
				if _, err := elem.To().WriteAt(p.data, p.offset); err != nil {
					fail(stop, errors.Wrap(err, "write output"))

					// keep draining so the fetcher can never block on a dead
					// consumer
					for range q {
					}
					return
				}

				if progress != nil {
					progress.PartDone(elem, p.index)
				}
			}
		}()

		go fetchOne(q, stop)
	}

	writers.Wait()

	return fetchEr
}
