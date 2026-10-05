package dl

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/utils"
)

type progress struct {
	pw       pw.Writer
	trackers *sync.Map // map[ID]*pw.Tracker
	opts     Options

	it *iter
}

func newProgress(p pw.Writer, it *iter, opts Options) *progress {
	return &progress{
		pw:       p,
		trackers: &sync.Map{},
		opts:     opts,
		it:       it,
	}
}

// OnAdd implements downloader.Progress. Each element owns its parts store.
func (p *progress) OnAdd(elem downloader.Elem) {
	tracker := prog.AppendTracker(p.pw, utils.Byte.FormatBinaryBytes, p.processMessage(elem), elem.File().Size())

	e := elem.(*iterElem)
	p.trackers.Store(e.id, tracker)
}

// Resume implements downloader.Resumer.
func (p *progress) Resume(elem downloader.Elem) (map[int]struct{}, int64, bool) {
	e := elem.(*iterElem)
	if e.parts == nil {
		return nil, 0, false
	}

	done := e.parts.Done()
	if len(done) == 0 {
		return nil, 0, false
	}

	return done, e.file.Size, true
}

// PartDone implements downloader.Resumer.
func (p *progress) PartDone(elem downloader.Elem, index int) {
	if parts := elem.(*iterElem).parts; parts != nil {
		parts.PartDone(index)
	}
}

// Reset implements downloader.Resumer.
func (p *progress) Reset(elem downloader.Elem) {
	if parts := elem.(*iterElem).parts; parts != nil {
		parts.Reset()
	}
}

func (p *progress) OnDownload(elem downloader.Elem, state downloader.ProgressState) {
	tracker, ok := p.trackers.Load(elem.(*iterElem).id)
	if !ok {
		return
	}

	t := tracker.(*pw.Tracker)
	t.UpdateTotal(state.Total)
	t.SetValue(state.Downloaded)
}

func (p *progress) OnDone(elem downloader.Elem, err error) {
	e := elem.(*iterElem)
	if !e.finalized {
		err = multierr.Combine(err, e.Finalize(err))
	}

	tracker, ok := p.trackers.Load(e.id)
	if !ok {
		return
	}
	t := tracker.(*pw.Tracker)
	defer p.trackers.Delete(e.id)
	if err != nil {
		if !errors.Is(err, context.Canceled) { // don't report user cancel
			p.fail(t, elem, errors.Wrap(err, "progress"))
		}
		// keep the partial file and its parts sidecar so the next run can
		// resume instead of starting over
		t.MarkAsErrored()
		return
	}

	p.it.Finish(e.logicalPos)
	p.it.Complete(e)
	t.MarkAsDone()
}

func (p *progress) fail(t *pw.Tracker, elem downloader.Elem, err error) {
	p.pw.Log(color.RedString("%s error: %s", p.elemString(elem), err.Error()))
	t.MarkAsErrored()
}

func (p *progress) processMessage(elem downloader.Elem) string {
	return p.elemString(elem)
}

func (p *progress) elemString(elem downloader.Elem) string {
	e := elem.(*iterElem)
	return fmt.Sprintf("%s(%d):%d -> %s",
		e.from.VisibleName(),
		e.from.ID(),
		e.fromMsg.ID,
		strings.TrimSuffix(e.to.Name(), tempExt))
}
