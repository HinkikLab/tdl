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
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
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
		return diagnostic.Describe(errors.New("download limit must be positive"), corei18n.Message{ID: "errors.message.download_limit_must_be_positive"})
	}
	if d.opts.Pool == nil {
		return diagnostic.Describe(errors.New("download pool is required"), corei18n.Message{ID: "errors.message.download_pool_is_required"})
	}
	if d.opts.Iter == nil {
		return diagnostic.Describe(errors.New("download iterator is required"), corei18n.Message{ID: "errors.message.download_iterator_is_required"})
	}
	if d.opts.Progress == nil {
		return diagnostic.Describe(errors.New("download progress is required"), corei18n.Message{ID: "errors.message.download_progress_is_required"})
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
			if finalizer, ok := elem.(FinalizingElem); ok {
				err = multierr.Append(err, finalizer.Finalize(err))
			}
			d.opts.Progress.OnDone(elem, err)

			if err != nil {
				// canceled by user, so we directly return error to stop all
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return diagnostic.Describe(errors.Wrap(err, "download"), corei18n.Message{ID: "errors.context.download", Args: map[string]any{"Reason": err}})
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
	// Finalizers and progress callbacks own files/state; always wait for workers.
	err := wg.Wait()
	if iterErr != nil {
		err = multierr.Append(err, diagnostic.Describe(errors.Wrap(iterErr, "iter"), corei18n.Message{ID: "errors.context.iter", Args: map[string]any{"Reason": iterErr}}))
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

	client := d.client(ctx, elem)

	threads := max(1, tutil.BestThreads(elem.File().Size(), d.opts.Threads))

	if parts := d.parts(elem); parts != nil {
		logctx.From(ctx).Debug("Resume partial download",
			zap.Int("parts", len(parts)),
			zap.Int("threads", threads))

		if err := d.parallelIgnore(ctx, client, elem, parts, threads); err != nil {
			return diagnostic.Describe(errors.Wrap(err, "download"), corei18n.Message{ID: "errors.context.download", Args: map[string]any{"Reason": err}})
		}
		return nil
	}

	w := newWriteAt(elem, d.opts.Progress, resumer(d.opts.Progress), MaxPartSize)
	_, err := downloader.NewDownloader().WithPartSize(MaxPartSize).
		Download(client, elem.File().Location()).
		WithThreads(threads).
		Parallel(ctx, w)
	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "download"), corei18n.Message{ID: "errors.context.download", Args: map[string]any{"Reason": err}})
	}
	if size := elem.File().Size(); size > 0 && w.downloaded.Load() != size {
		downloaded := w.downloaded.Load()
		return diagnostic.Describe(errors.Errorf("incomplete download: got %d bytes, expected %d", downloaded, size), corei18n.Message{ID: "errors.message.incomplete_download_got_value_bytes_expected_value", Args: map[string]any{"Arg1": downloaded, "Arg2": size}})
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
