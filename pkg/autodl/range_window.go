package autodl

import (
	"context"
	"fmt"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/logctx"
)

func countMissingRange(state *State, start, end int) int {
	if end <= start {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	finished := 0
	for id := range state.finished {
		if id >= start && id < end {
			finished++
		}
	}
	return end - start - finished
}

func countPendingRange(state *State, start, end int) int {
	if end <= start {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	terminal := 0
	for id := range state.skipped {
		if id >= start && id < end {
			terminal++
		}
	}
	for id := range state.filtered {
		if id >= start && id < end {
			terminal++
		}
	}
	return end - start - terminal
}

// runRangeWindow keeps only the next RPC's IDs in memory. Incremental windows
// retain their bounded explicit IDs because they also represent scan coverage.
func (r *Runner) runRangeWindow(ctx context.Context, job *Job, link Link, dir string, store *stateStore, state *State, threads, limit int) error {
	start, end := num(job.StartComment), num(job.EndComment)
	r.setTargets(end - start)
	count := countPendingRange(state, start, end)
	color.Cyan("Target range: %d-%d (%d)", start, end-1, end-start)
	color.Cyan("Recorded as finished: %d, to validate/download: %d", state.Len(), count)
	logctx.From(ctx).Info("Plan", zap.Int("targets", end-start), zap.Int("missing", count))
	if r.opts.CheckOnly {
		color.Yellow("Check only: skipping the download step")
		return nil
	}
	if count == 0 {
		color.Green("Everything is already downloaded")
		return nil
	}
	if err := r.downloadSelection(ctx, job, link, dir, nil, start, end, store, state, threads, limit); err != nil {
		return err
	}
	if left := countMissingRange(state, start, end); left > 0 {
		if !r.confirm(ctx, fmt.Sprintf("Retry the %d missing message(s) now?", left), true) {
			return errors.Errorf("%d message(s) still missing", left)
		}
		if err := r.downloadSelection(ctx, job, link, dir, nil, start, end, store, state, threads, limit); err != nil {
			return err
		}
		if left = countMissingRange(state, start, end); left > 0 {
			return errors.Errorf("%d message(s) still missing", left)
		}
	}
	return nil
}
