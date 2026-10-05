package autodl

import (
	"context"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/diagnostic"
	tdl "github.com/iyear/tdl/core/downloader"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/internal/transfer"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/messages"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/utils"
)

func (r *Runner) download(ctx context.Context, job *Job, link Link, dir string, ids []int,
	store *stateStore, state *State, threads, limit int,
) error {
	return r.downloadSelection(ctx, job, link, dir, ids, 0, 0, store, state, threads, limit)
}

func (r *Runner) downloadSelection(ctx context.Context, job *Job, link Link, dir string, ids []int,
	start, end int, store *stateStore, state *State, threads, limit int,
) error {
	count := len(ids)
	if end > start {
		count = countPendingRange(state, start, end)
	}
	dialog, err := r.resolveDialog(ctx, job, link)
	if err != nil {
		return err
	}

	log := logctx.From(ctx)

	pw := prog.NewContext(ctx, utils.Byte.FormatBinaryBytes)

	jobCtx := &jobContext{
		job:         job,
		state:       state,
		store:       store,
		logger:      log,
		onCommitted: func(el *elem) { r.observeCounts(transfer.Counts{FilesDownloaded: 1, Bytes: el.file.Size()}) },
		onFailed:    func(el *elem) { r.observeCounts(transfer.Counts{FilesFailed: 1}) },
	}
	progress := newJobProgress(pw, jobCtx)

	opts := &iterOptions{
		state:        state,
		rangeStart:   start,
		rangeEnd:     end,
		template:     r.template(),
		include:      r.opts.Include,
		exclude:      r.opts.Exclude,
		takeout:      r.opts.Takeout,
		batch:        DefaultBatchSize,
		delay:        r.opts.Delay,
		reservations: &r.reservations,
		onCounts:     r.observeCounts,
		onFinish: func(ids []int) {
			state.Finish(ids...)

			if serr := store.SaveThrottled(); serr != nil {
				log.Warn("Save state", zap.Error(serr))
			}
		},
		onSkip: func(ids []int) {
			state.Skip(ids...)
			progress.markSkipped(len(ids))

			if serr := store.SaveThrottled(); serr != nil {
				log.Warn("Save state", zap.Error(serr))
			}
		},
	}
	if end > start {
		opts.isFinished = state.IsTerminalWithoutMedia
	}

	it, err := newIter(r.pool, r.manager, dialog, dir, ids, opts)
	if err != nil {
		return err
	}
	stopRender := prog.Start(pw)
	defer stopRender()

	dl := tdl.New(tdl.Options{
		Pool:     r.pool,
		Threads:  threads,
		Iter:     it,
		Progress: progress,
	})
	// part level resume: a failed element keeps its temp file, the next run
	// only fetches the missing parts
	dl.SetSkipParts(true)

	log.Info("Start download",
		zap.Int("ids", count),
		zap.Int("threads", threads),
		zap.Int("limit", limit))

	err = dl.Download(ctx, limit)

	multierr.AppendInto(&err, it.Drain())

	stopRender()
	multierr.AppendInto(&err, progress.Err())

	if serr := store.Save(); serr != nil {
		log.Warn("Save state", zap.Error(serr))
		multierr.AppendInto(&err, serr)
	}

	done, failed, skipped := progress.Stats()

	// A whole range resolving to nothing is almost never real: it means the ids
	// were looked up in the wrong dialog, typically comment ids that were
	// resolved against the channel instead of its discussion group.
	if count > 0 && done == 0 && failed == 0 && skipped >= count {
		color.Red("%s", console.Translate(ctx, messages.DownloadNoneFound(count, dialog.ID())))
		color.Red("%s", console.Translate(ctx, messages.DownloadNoneFoundHint()))
		log.Warn("Every target was unavailable",
			zap.Int("ids", count),
			zap.Int64("dialog", dialog.ID()),
			zap.Bool("comment_mode", commentDialog(job, link)))
	}

	if err != nil {
		return diagnostic.Describe(errors.Wrap(err, "download"), corei18n.Message{ID: "errors.context.download", Args: map[string]any{"Reason": err}})
	}

	if itErr := it.Err(); itErr != nil {
		return diagnostic.Describe(errors.Wrap(itErr, "iterate"), corei18n.Message{ID: "errors.context.iterate", Args: map[string]any{"Reason": itErr}})
	}

	return nil
}
