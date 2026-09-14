package downloader

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/downloader"
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
	wg, wgctx := errgroup.WithContext(ctx)
	wg.SetLimit(limit)

	for d.opts.Iter.Next(wgctx) {
		elem := d.opts.Iter.Value()

		wg.Go(func() (rerr error) {
			d.opts.Progress.OnAdd(elem)
			defer func() { d.opts.Progress.OnDone(elem, rerr) }()

			if err := d.download(wgctx, elem); err != nil {
				// canceled by user, so we directly return error to stop all
				if errors.Is(err, context.Canceled) {
					return errors.Wrap(err, "download")
				}

				// don't return error, just log it
				logctx.
					From(ctx).
					Error("Download error",
						zap.Any("element", elem),
						zap.Error(err),
					)
			}

			return nil
		})
	}

	if err := d.opts.Iter.Err(); err != nil {
		return errors.Wrap(err, "iter")
	}

	return wg.Wait()
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

	threads := tutil.BestThreads(elem.File().Size(), d.opts.Threads)

	if parts := d.parts(elem); parts != nil {
		logctx.From(ctx).Debug("Resume partial download",
			zap.Int("parts", len(parts)),
			zap.Int("threads", threads))

		return errors.Wrap(d.parallelIgnore(ctx, client, elem, parts, threads), "download")
	}

	_, err := downloader.NewDownloader().WithPartSize(MaxPartSize).
		Download(client, elem.File().Location()).
		WithThreads(threads).
		Parallel(ctx, newWriteAt(elem, d.opts.Progress, resumer(d.opts.Progress), MaxPartSize))
	if err != nil {
		return errors.Wrap(err, "download")
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
