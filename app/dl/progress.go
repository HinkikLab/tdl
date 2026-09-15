package dl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/gabriel-vasile/mimetype"
	"github.com/go-faster/errors"
	pw "github.com/jedib0t/go-pretty/v6/progress"

	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/util/fsutil"
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

	tracker, ok := p.trackers.Load(e.id)
	if !ok {
		return
	}
	t := tracker.(*pw.Tracker)
	defer p.trackers.Delete(e.id)
	if e.parts != nil {
		if flushErr := e.parts.Flush(); flushErr != nil && err == nil {
			err = errors.Wrap(flushErr, "save partial progress")
		}
	}

	if err := e.to.Close(); err != nil {
		p.fail(t, elem, errors.Wrap(err, "close file"))
		return
	}

	if err != nil {
		if !errors.Is(err, context.Canceled) { // don't report user cancel
			p.fail(t, elem, errors.Wrap(err, "progress"))
		}
		// keep the partial file and its parts sidecar so the next run can
		// resume instead of starting over
		t.MarkAsErrored()
		return
	}

	if err := p.donePost(e); err != nil {
		p.fail(t, elem, errors.Wrap(err, "post file"))
		return
	}
	p.it.Finish(e.logicalPos)
	t.MarkAsDone()
}

func (p *progress) donePost(elem *iterElem) error {
	newfile := strings.TrimSuffix(filepath.Base(elem.to.Name()), tempExt)

	if p.opts.RewriteExt {
		mime, err := mimetype.DetectFile(elem.to.Name())
		if err != nil {
			return errors.Wrap(err, "detect mime")
		}
		ext := mime.Extension()
		if ext != "" && (filepath.Ext(newfile) != ext) {
			newfile = fsutil.GetNameWithoutExt(newfile) + ext
		}
	}

	newpath := filepath.Join(filepath.Dir(elem.to.Name()), newfile)
	if err := os.Rename(elem.to.Name(), newpath); err != nil {
		return errors.Wrap(err, "rename file")
	}

	// the element is complete, its parts are not needed anymore
	if elem.parts != nil {
		elem.parts.Remove()
	}

	// Set file modification time to message date if available
	if elem.file.Date > 0 {
		fileTime := time.Unix(elem.file.Date, 0)
		if err := os.Chtimes(newpath, fileTime, fileTime); err != nil {
			// The payload is already complete and renamed. A filesystem that
			// rejects timestamp metadata must not make it download again.
			p.pw.Log(color.YellowString("%s warning: set file time: %s", newpath, err))
		}
	}

	return nil
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
