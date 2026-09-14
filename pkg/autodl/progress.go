package autodl

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	pw "github.com/jedib0t/go-pretty/v6/progress"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/utils"
)

// JobLogger is the subset of the tdl logger the batch mode needs.
type JobLogger interface {
	Info(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
	Error(msg string, fields ...zap.Field)
	Debug(msg string, fields ...zap.Field)
}

// jobContext is the state a progress handler needs.
type jobContext struct {
	job    *Job
	state  *State
	store  *stateStore
	logger JobLogger
}

// jobProgress renders the progress of one job and commits every finished
// element to the state store, so an interrupted run resumes where it stopped.
type jobProgress struct {
	pw pw.Writer

	mu       sync.Mutex
	trackers map[*elem]*pw.Tracker
	ctx      *jobContext

	done    int
	failed  int
	skipped int
}

func newJobProgress(w pw.Writer, ctx *jobContext) *jobProgress {
	return &jobProgress{
		pw:       w,
		trackers: make(map[*elem]*pw.Tracker),
		ctx:      ctx,
	}
}

// OnAdd implements downloader.Progress.
func (p *jobProgress) OnAdd(e downloader.Elem) {
	el := e.(*elem)

	tracker := prog.AppendTracker(p.pw, utils.Byte.FormatBinaryBytes, el.path, el.file.Size())

	p.mu.Lock()
	p.trackers[el] = tracker
	p.mu.Unlock()
}

// onWrite feeds the progress bar. It is called from the download workers.
func (p *jobProgress) onWrite(el *elem) {
	p.mu.Lock()
	tracker := p.trackers[el]
	p.mu.Unlock()

	if tracker != nil {
		tracker.SetValue(el.written.Load())
	}
}

// OnDownload implements downloader.Progress.
func (p *jobProgress) OnDownload(e downloader.Elem, state downloader.ProgressState) {
	p.mu.Lock()
	tracker := p.trackers[e.(*elem)]
	p.mu.Unlock()

	if tracker != nil {
		tracker.SetValue(state.Downloaded)
	}
}

// OnDone implements downloader.Progress. It runs after an element settled, so
// this is where its verdict is written to the state.
func (p *jobProgress) OnDone(e downloader.Elem, err error) {
	el := e.(*elem)

	p.mu.Lock()
	tracker := p.trackers[el]
	delete(p.trackers, el)
	p.mu.Unlock()

	if err == nil {
		err = el.finish()
		if err == nil {
			// keep the message date as the file time, like the regular
			// downloader does
			if el.date > 0 {
				ts := time.Unix(el.date, 0)
				_ = os.Chtimes(el.path, ts, ts)
			}

			p.mu.Lock()
			p.done++
			p.mu.Unlock()

			p.ctx.state.Finish(el.msgID)
			if serr := p.ctx.store.SaveThrottled(); serr != nil {
				p.ctx.logger.Warn("Save state", zap.Error(serr))
			}

			if tracker != nil {
				tracker.MarkAsDone()
			}
			return
		}
	}

	el.cleanupFile()

	p.mu.Lock()
	p.failed++
	p.mu.Unlock()

	if tracker != nil {
		tracker.MarkAsErrored()
	}

	p.ctx.logger.Error("Download error",
		zap.String("file", filepath.Base(el.path)),
		zap.Int("message_id", el.msgID),
		zap.Error(err))
}

// markSkipped counts messages that have no downloadable media.
func (p *jobProgress) markSkipped(n int) {
	p.mu.Lock()
	p.skipped += n
	p.mu.Unlock()
}

// Stats returns the number of done, failed and skipped messages.
func (p *jobProgress) Stats() (done, failed, skipped int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.done, p.failed, p.skipped
}
