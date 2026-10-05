package autodl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/internal/transfer"
	"github.com/iyear/tdl/pkg/tplfunc"
	"github.com/iyear/tdl/pkg/utils"
)

// nameTemplate renders the download file name.
type nameTemplate struct {
	tpl *template.Template
}

func newNameTemplate(tpl string) (*nameTemplate, error) {
	parsed, err := template.New("autodl").
		Funcs(tplFuncMap()).
		Parse(tpl)
	if err != nil {
		return nil, errors.Wrap(err, "parse template")
	}

	return &nameTemplate{tpl: parsed}, nil
}

func (n *nameTemplate) execute(data *fileTemplate) (string, error) {
	buf := bytes.Buffer{}
	if err := n.tpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// tplFuncMap returns the template helpers shared with the regular downloader.
func tplFuncMap() template.FuncMap { return tplfunc.FuncMap(tplfunc.All...) }

// fileTemplate is the data available to the download name template. It mirrors
// the fields of the regular tdl downloader so the same template works in both
// modes.
type fileTemplate struct {
	DialogID     int64
	MessageID    int
	MessageDate  int64
	FileName     string
	FileCaption  string
	FileSize     string
	DownloadDate int64
}

// iter walks the message ids of one job and produces downloadable elements in
// batches instead of one Telegram request per message.
type iter struct {
	pool    dcpool.Pool
	manager *peers.Manager
	dialog  peers.Peer
	dir     string
	ids     []int
	opts    *iterOptions

	tpl          *nameTemplate
	reservations *transfer.Reservations
	delay        transfer.Delay
	include      map[string]struct{}
	exclude      map[string]struct{}

	cursor      int
	rangeCursor int
	pending     []*tg.Message

	mu       sync.Mutex
	finished map[int]struct{}

	elems chan downloader.Elem
	err   error

	stopped bool
}

// iterOptions carries the per job download settings.
type iterOptions struct {
	state *State
	// A configured interval is consumed in bounded batches without an ID list.
	rangeStart, rangeEnd int
	isFinished           func(int) bool
	template             string
	include              []string
	exclude              []string
	takeout              bool
	batch                int
	delay                time.Duration
	reservations         *transfer.Reservations
	onCounts             func(transfer.Counts)
	// onSkip is called with the ids that turned out to have no media.
	onSkip func(ids []int)
	// onFinish is called with the ids that are already downloaded.
	onFinish func(ids []int)
}

// newIter creates the download iterator of one job.
func newIter(pool dcpool.Pool, manager *peers.Manager, dialog peers.Peer, dir string, ids []int, opts *iterOptions) (*iter, error) {
	tpl, err := newNameTemplate(opts.template)
	if err != nil {
		return nil, err
	}

	include := make(map[string]struct{}, len(opts.include))
	for _, e := range opts.include {
		include[strings.ToLower(fsutil.AddPrefixDot(e))] = struct{}{}
	}

	exclude := make(map[string]struct{}, len(opts.exclude))
	for _, e := range opts.exclude {
		exclude[strings.ToLower(fsutil.AddPrefixDot(e))] = struct{}{}
	}

	ids = append([]int(nil), ids...)
	sort.Ints(ids)
	ids = slices.Compact(ids)

	batch := opts.batch
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	batch = min(batch, DefaultBatchSize)
	opts.batch = batch

	reservations := opts.reservations
	if reservations == nil {
		reservations = &transfer.Reservations{}
	}
	return &iter{
		pool:         pool,
		manager:      manager,
		dialog:       dialog,
		dir:          dir,
		ids:          ids,
		rangeCursor:  opts.rangeStart,
		opts:         opts,
		tpl:          tpl,
		reservations: reservations,
		delay:        transfer.Delay{Duration: opts.delay},
		include:      include,
		exclude:      exclude,
		finished:     make(map[int]struct{}),
		elems:        make(chan downloader.Elem, 1),
	}, nil
}

// Next implements downloader.Iter.
func (i *iter) Next(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		i.err = ctx.Err()
		return false
	default:
	}

	if len(i.elems) > 0 {
		return true
	}

	return i.process(ctx)
}

// process resolves the next batch of messages.
func (i *iter) process(ctx context.Context) bool {
	if i.err != nil || i.stopped {
		return false
	}

	for len(i.pending) > 0 || i.cursor < len(i.ids) || i.rangeCursor < i.opts.rangeEnd {
		if len(i.pending) > 0 {
			msg := i.pending[0]
			i.pending[0] = nil
			i.pending = i.pending[1:]
			if i.isFinished(msg.ID) {
				continue
			}
			if i.push(ctx, i.dialog, msg) {
				return true
			}
			if i.err != nil {
				return false
			}
			continue
		}
		var batch []int
		if i.opts.rangeEnd > i.opts.rangeStart {
			batch = make([]int, 0, i.opts.batch)
			for i.rangeCursor < i.opts.rangeEnd && len(batch) < i.opts.batch {
				id := i.rangeCursor
				i.rangeCursor++
				if !i.isFinished(id) {
					batch = append(batch, id)
				}
			}
			if len(batch) == 0 {
				continue
			}
		} else {
			end := min(i.cursor+i.opts.batch, len(i.ids))
			batch = i.ids[i.cursor:end]
			i.cursor = end
		}

		found, gone, err := tutil.GetMessages(ctx, i.pool.Default(ctx), i.dialog.InputPeer(), batch)
		if err != nil {
			i.err = errors.Wrap(err, "resolve messages")
			return false
		}

		if len(gone) > 0 {
			i.count(transfer.Counts{Messages: int64(len(gone)), MessagesUnavailable: int64(len(gone))})
			i.markSkipped(ctx, gone)
		}

		for _, id := range batch {
			msg, ok := found[id]
			if !ok {
				continue // deleted, already handled above
			}

			i.pending = append(i.pending, msg)
		}
	}

	i.stopped = true
	return false
}

// push turns one message into a download element. It reports whether an
// element was queued.
func (i *iter) push(ctx context.Context, from peers.Peer, msg *tg.Message) bool {
	id := msg.ID

	item, ok := tmedia.GetMedia(msg)
	i.count(transfer.Counts{Messages: 1})
	if !ok {
		i.count(transfer.Counts{MessagesNoMedia: 1})
		i.markSkipped(ctx, []int{id})
		return false
	}
	i.count(transfer.Counts{Files: 1})

	ext := strings.ToLower(filepath.Ext(item.Name))
	if len(i.include) > 0 {
		if _, ok := i.include[ext]; !ok {
			i.count(transfer.Counts{FilesFiltered: 1})
			if i.opts.state != nil {
				i.opts.state.Filter(id)
			}
			i.markDone(id)
			return false
		}
	}
	if len(i.exclude) > 0 {
		if _, ok := i.exclude[ext]; ok {
			i.count(transfer.Counts{FilesFiltered: 1})
			if i.opts.state != nil {
				i.opts.state.Filter(id)
			}
			i.markDone(id)
			return false
		}
	}

	name, err := i.tpl.execute(&fileTemplate{
		DialogID:     peerID(from),
		MessageID:    id,
		MessageDate:  item.Date,
		FileName:     item.Name,
		FileCaption:  msg.Message,
		FileSize:     utils.Byte.FormatBinaryBytes(item.Size),
		DownloadDate: time.Now().Unix(),
	})
	if err != nil {
		i.err = errors.Wrap(err, "execute template")
		return false
	}

	path, err := fsutil.JoinWithin(i.dir, name)
	if err != nil {
		i.err = err
		return false
	}
	identity, err := downloader.FileIdentityOf(mediaFile{item, peerID(from), id})
	if err != nil {
		i.err = err
		return false
	}
	path, err = i.reservations.Reserve(path, fmt.Sprintf("%s/message:%d/media:%v", peerKey(from), id, identity))
	if err != nil {
		i.err = err
		return false
	}
	record, recorded := MediaRecord{}, false
	if i.opts.state != nil {
		record, recorded = i.opts.state.Media(id)
	}
	stat, statErr := os.Stat(path)
	compatible := !recorded || record.Identity == identity && record.Path == canonicalPath(path) && record.MatchesStat(stat)
	if compatible && statErr == nil && stat.Mode().IsRegular() && stat.Size() == item.Size {
		// Only trust a pre-existing file after resolving the message in a batch
		// and comparing it with Telegram's authoritative size.
		if i.opts.state != nil {
			if err := i.opts.state.CompleteMedia(id, identity, path); err != nil {
				i.err = err
				return false
			}
		}
		i.count(transfer.Counts{FilesExisting: 1})
		i.markDone(id)
		return false
	}
	if i.opts.state != nil {
		i.opts.state.ForgetMedia(id)
	}
	if recorded && !compatible {
		if statErr == nil && stat.Mode().IsRegular() {
			backup, err := transfer.PreserveFile(path)
			if err != nil {
				i.err = err
				return false
			}
			logctx.From(ctx).Warn("Changed completed media preserved; downloading current identity", zap.String("recovery_path", backup))
		}
	}

	e := newElem(from, id, mediaFile{item, peerID(from), id}, item.Date, path)
	if err := i.delay.Wait(ctx); err != nil {
		i.err = err
		return false
	}
	if err = e.start(i.opts.takeout); err != nil {
		i.err = errors.Wrap(err, "create file")
		return false
	}
	if paths := e.store.RecoveryPaths(); len(paths) > 0 {
		logctx.From(ctx).Warn("Unverified partial download preserved; downloading this media again", zap.Strings("recovery_paths", paths))
	}

	select {
	case <-ctx.Done():
		i.err = multierr.Append(ctx.Err(), e.closeFile())
		return false
	case i.elems <- e:
	}

	return true
}

// Value implements downloader.Iter.
func (i *iter) Value() downloader.Elem { return <-i.elems }

// Drain discards the elements that were queued but never consumed and closes
// the queue.
//
// The downloader stops asking for elements as soon as the iterator fails, so
// the queued files would otherwise keep their progress trackers alive forever.
// It must only be called after the download has returned.
func (i *iter) Drain() (err error) {
	for {
		select {
		case e, ok := <-i.elems:
			if !ok {
				return err
			}
			multierr.AppendInto(&err, e.(*elem).closeFile())
		default:
			close(i.elems)
			return err
		}
	}
}

// Err implements downloader.Iter.
func (i *iter) Err() error { return i.err }

// SetFinished seeds the resume set.
func (i *iter) SetFinished(ids []int) {
	i.mu.Lock()
	defer i.mu.Unlock()

	for _, id := range ids {
		i.finished[id] = struct{}{}
	}
}

// Finished returns the ids that were downloaded during this run.
func (i *iter) Finished() []int {
	i.mu.Lock()
	defer i.mu.Unlock()

	out := make([]int, 0, len(i.finished))
	for id := range i.finished {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// markDone records an id as handled.
func (i *iter) markDone(id int) {
	if i.opts.isFinished == nil {
		i.mu.Lock()
		i.finished[id] = struct{}{}
		i.mu.Unlock()
	}

	if i.opts.onFinish != nil {
		i.opts.onFinish([]int{id})
	}
}

// markSkipped records ids that have no media, e.g. deleted messages.
func (i *iter) markSkipped(ctx context.Context, ids []int) {
	if i.opts.isFinished == nil {
		i.mu.Lock()
		for _, id := range ids {
			i.finished[id] = struct{}{}
		}
		i.mu.Unlock()
	}

	if len(ids) > 0 {
		logctx.From(ctx).Info("Skip unavailable messages",
			zap.Int("count", len(ids)),
			zap.Ints("ids", ids))
	}

	if i.opts.onSkip != nil {
		i.opts.onSkip(ids)
	}
}

func (i *iter) isFinished(id int) bool {
	if i.opts.isFinished != nil {
		return i.opts.isFinished(id)
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	_, ok := i.finished[id]
	return ok
}

func peerID(p peers.Peer) int64 { return p.ID() }

func (i *iter) count(counts transfer.Counts) {
	if i.opts.onCounts != nil {
		i.opts.onCounts(counts)
	}
}
