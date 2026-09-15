package downloader

import (
	"context"
	"sync"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/downloader"
	"go.uber.org/multierr"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/util/tutil"
)

// MaxPartSize refer to https://core.telegram.org/api/files#downloading-files
const MaxPartSize = 1024 * 1024

type Downloader struct {
	opts Options
}

type Options struct {
	Pool     dcpool.Pool
	Threads  int
	Iter     Iter
	Progress Progress

	// SkipParts enables part-level resume. It requires Progress to implement
	// Resumer as well. When enabled, a failed element keeps its partial temp
	// file and the next run only downloads the missing parts.
	SkipParts bool
}

func New(opts Options) *Downloader {
	return &Downloader{
		opts: opts,
	}
}

// SetSkipParts enables or disables part-level resume.
//
// It requires the progress implementation to implement Resumer as well.
func (d *Downloader) SetSkipParts(skip bool) {
	d.opts.SkipParts = skip
}

func (d *Downloader) Download(ctx context.Context, limit int) error {
	if limit <= 0 {
		return errors.New("download limit must be positive")
	}
	if d.opts.Pool == nil {
		return errors.New("download pool is required")
	}
	if d.opts.Iter == nil {
		return errors.New("download iterator is required")
	}
	if d.opts.Progress == nil {
		return errors.New("download progress is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wg, wgctx := errgroup.WithContext(ctx)
	wg.SetLimit(limit)
	var failureMu sync.Mutex
	var failures error

	for d.opts.Iter.Next(wgctx) {
		elem := d.opts.Iter.Value()

		wg.Go(func() error {
			d.opts.Progress.OnAdd(elem)
			err := d.download(wgctx, elem)
			d.opts.Progress.OnDone(elem, err)

			if err != nil {
				// canceled by user, so we directly return error to stop all
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return errors.Wrap(err, "download")
				}

				// Continue independent files, but report every failure after all
				// scheduled downloads have settled.
				logctx.
					From(ctx).
					Error("Download error",
						zap.Any("element", elem),
						zap.Error(err),
					)
				failureMu.Lock()
				failures = multierr.Append(failures, err)
				failureMu.Unlock()
			}

			return nil
		})
	}

	iterErr := d.opts.Iter.Err()
	if iterErr != nil {
		cancel()
	}
	// Progress callbacks own files and state; wait even if resolution fails.
	err := wg.Wait()
	if iterErr != nil {
		err = multierr.Append(err, errors.Wrap(iterErr, "iter"))
	}
	failureMu.Lock()
	err = multierr.Append(err, failures)
	failureMu.Unlock()
	return err
}

func (d *Downloader) download(ctx context.Context, elem Elem) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	logctx.From(ctx).Debug("Start download elem",
		zap.Any("elem", elem))

	client := d.opts.Pool.Client(ctx, elem.File().DC())
	if elem.AsTakeout() {
		client = d.opts.Pool.Takeout(ctx, elem.File().DC())
	}

	threads := max(1, tutil.BestThreads(elem.File().Size(), d.opts.Threads))

	if parts := d.parts(elem); parts != nil {
		logctx.From(ctx).Debug("Resume partial download",
			zap.Int("parts", len(parts)),
			zap.Int("threads", threads))

		if err := d.parallelIgnore(ctx, client, elem, parts, threads); err != nil {
			return errors.Wrap(err, "download")
		}
		return nil
	}

	w := newWriteAt(elem, d.opts.Progress, resumer(d.opts.Progress), MaxPartSize)
	_, err := downloader.NewDownloader().WithPartSize(MaxPartSize).
		Download(client, elem.File().Location()).
		WithThreads(threads).
		Parallel(ctx, w)
	if err != nil {
		return errors.Wrap(err, "download")
	}
	if size := elem.File().Size(); size > 0 && w.downloaded.Load() != size {
		return errors.Errorf("incomplete download: got %d bytes, expected %d", w.downloaded.Load(), size)
	}

	return nil
}

// parts returns the set of parts that are already on disk and can be skipped.
//
// Parts are only tracked when the progress implementation opts in via Resumer
// and SkipParts is enabled, otherwise nil is returned and the whole file is
// downloaded from the beginning.
func (d *Downloader) parts(elem Elem) map[int]struct{} {
	resumer, ok := d.opts.Progress.(Resumer)
	if !ok || !d.opts.SkipParts {
		return nil
	}

	if partsCount(elem.File().Size()) <= 1 { // single part files are cheap to restart
		return nil
	}

	done, _, ok := resumer.Resume(elem)
	if !ok || len(done) == 0 {
		return nil
	}

	return done
}

func resumer(p Progress) Resumer {
	r, _ := p.(Resumer)
	return r
}

func partsCount(size int64) int {
	if size <= 0 {
		return 0
	}
	return int((size + MaxPartSize - 1) / MaxPartSize)
}
