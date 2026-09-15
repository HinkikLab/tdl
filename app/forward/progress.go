package forward

import (
	"fmt"
	"strings"
	"sync"

	"github.com/fatih/color"
	pw "github.com/jedib0t/go-pretty/v6/progress"
	"github.com/mattn/go-runewidth"

	"github.com/iyear/tdl/core/forwarder"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/utils"
)

type progress struct {
	pw        pw.Writer
	trackers  *sync.Map // map[tuple]*pw.Tracker
	trackerMu sync.Mutex
	nameMu    sync.Mutex
	elemName  map[int64]string
}

type tuple struct {
	from int64
	msg  int
	to   int64
}

func newProgress(p pw.Writer) *progress {
	return &progress{
		pw:       p,
		trackers: &sync.Map{},
		elemName: make(map[int64]string),
	}
}

func (p *progress) OnAdd(elem forwarder.Elem) {
	tracker := prog.AppendTracker(p.pw, pw.FormatNumber, p.processMessage(elem, false), 1)
	p.trackers.Store(p.tuple(elem), tracker)
}

func (p *progress) OnClone(elem forwarder.Elem, state forwarder.ProgressState) {
	p.trackerMu.Lock()
	defer p.trackerMu.Unlock()
	trackerValue, ok := p.trackers.Load(p.tuple(elem))
	if !ok {
		return
	}
	tracker := trackerValue.(*pw.Tracker)

	// display re-upload transfer info
	tracker.Units.Formatter = utils.Byte.FormatBinaryBytes
	tracker.UpdateMessage(p.processMessage(elem, true))
	tracker.UpdateTotal(state.Total)
	tracker.SetValue(state.Done)
}

func (p *progress) OnDone(elem forwarder.Elem, err error) {
	p.trackerMu.Lock()
	defer p.trackerMu.Unlock()
	key := p.tuple(elem)
	trackerValue, ok := p.trackers.LoadAndDelete(key)
	if !ok {
		return
	}
	tracker := trackerValue.(*pw.Tracker)

	if err != nil {
		p.pw.Log(color.RedString("%s error: %s", p.metaString(elem), err.Error()))
		tracker.MarkAsErrored()
		return
	}

	if tracker.Total == 1 {
		tracker.Increment(1)
	}
	tracker.MarkAsDone()
}

func (p *progress) tuple(elem forwarder.Elem) tuple {
	return tuple{
		from: elem.From().ID(),
		msg:  elem.Msg().ID,
		to:   elem.To().ID(),
	}
}

func (p *progress) processMessage(elem forwarder.Elem, clone bool) string {
	b := &strings.Builder{}

	b.WriteString(p.metaString(elem))
	if clone {
		b.WriteString(" [clone]")
	}

	return b.String()
}

func (p *progress) metaString(elem forwarder.Elem) string {
	p.nameMu.Lock()
	defer p.nameMu.Unlock()

	// TODO(iyear): better responsive name
	if _, ok := p.elemName[elem.From().ID()]; !ok {
		p.elemName[elem.From().ID()] = runewidth.Truncate(elem.From().VisibleName(), 15, "...")
	}
	if _, ok := p.elemName[elem.To().ID()]; !ok {
		p.elemName[elem.To().ID()] = runewidth.Truncate(elem.To().VisibleName(), 15, "...")
	}

	return fmt.Sprintf("%s(%d):%d -> %s(%d)",
		p.elemName[elem.From().ID()],
		elem.From().ID(),
		elem.Msg().ID,
		p.elemName[elem.To().ID()],
		elem.To().ID())
}
