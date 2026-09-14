package autodl

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/tplfunc"
	"github.com/iyear/tdl/pkg/utils"
)

// nameTemplate renders the download file name.
type nameTemplate struct {
	tpl *template.Template
	// literal is set when the template does not depend on the message at all.
	// Only then can the list of existing files be built without resolving any
	// message first.
	literal string
}

func newNameTemplate(tpl string) (*nameTemplate, error) {
	parsed, err := template.New("autodl").
		Funcs(tplFuncMap()).
		Parse(tpl)
	if err != nil {
		return nil, errors.Wrap(err, "parse template")
	}

	nt := &nameTemplate{tpl: parsed}

	name, err := nt.execute(&fileTemplate{})
	if err == nil && !strings.Contains(name, "{") {
		nt.literal = name
	}

	return nt, nil
}

func (n *nameTemplate) execute(data *fileTemplate) (string, error) {
	buf := bytes.Buffer{}
	if err := n.tpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// idsOnly reports whether the file name depends on nothing but the dialog id
// and the message id, which makes it predictable before the message is
// resolved.
func (n *nameTemplate) idsOnly() bool {
	fields := make(map[string]struct{})
	collectFields(n.tpl.Tree.Root, fields)

	for f := range fields {
		if f != "DialogID" && f != "MessageID" {
			return false
		}
	}

	return len(fields) > 0
}

// collectFields walks a template tree and records every .Field it references.
func collectFields(node parse.Node, out map[string]struct{}) {
	switch n := node.(type) {
	case nil:
		return
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, child := range n.Nodes {
			collectFields(child, out)
		}
	case *parse.ActionNode:
		collectFields(n.Pipe, out)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for _, cmd := range n.Cmds {
			collectFields(cmd, out)
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			collectFields(arg, out)
		}
	case *parse.FieldNode:
		if len(n.Ident) > 0 {
			out[n.Ident[0]] = struct{}{}
		}
	case *parse.ChainNode:
		collectFields(n.Node, out)
	case *parse.IfNode:
		collectFields(n.Pipe, out)
		collectFields(n.List, out)
		collectFields(n.ElseList, out)
	case *parse.RangeNode:
		collectFields(n.Pipe, out)
		collectFields(n.List, out)
		collectFields(n.ElseList, out)
	case *parse.WithNode:
		collectFields(n.Pipe, out)
		collectFields(n.List, out)
		collectFields(n.ElseList, out)
	case *parse.TemplateNode:
		if n.Pipe != nil {
			collectFields(n.Pipe, out)
		}
	case *parse.VariableNode:
		for _, ident := range n.Ident {
			out[ident] = struct{}{}
		}
	}
}

// name renders the predictable name of a message.
func (n *nameTemplate) name(dialogID int64, msgID int) string {
	out, err := n.execute(&fileTemplate{DialogID: dialogID, MessageID: msgID})
	if err != nil {
		return ""
	}

	return out
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

	tpl     *nameTemplate
	include map[string]struct{}
	exclude map[string]struct{}

	cursor  int
	pending []*tg.Message

	mu       sync.Mutex
	finished map[int]struct{}

	elems chan downloader.Elem
	err   error

	stopped bool
}

// iterOptions carries the per job download settings.
type iterOptions struct {
	template string
	include  []string
	exclude  []string
	takeout  bool
	batch    int
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
		include[strings.ToLower(e)] = struct{}{}
	}

	exclude := make(map[string]struct{}, len(opts.exclude))
	for _, e := range opts.exclude {
		exclude[strings.ToLower(e)] = struct{}{}
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

	return &iter{
		pool:     pool,
		manager:  manager,
		dialog:   dialog,
		dir:      dir,
		ids:      ids,
		opts:     opts,
		tpl:      tpl,
		include:  include,
		exclude:  exclude,
		finished: make(map[int]struct{}),
		elems:    make(chan downloader.Elem, 1),
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

	for len(i.pending) > 0 || i.cursor < len(i.ids) {
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
		end := min(i.cursor+i.opts.batch, len(i.ids))
		batch := i.ids[i.cursor:end]
		i.cursor = end

		found, gone, err := tutil.GetMessages(ctx, i.pool.Default(ctx), i.dialog.InputPeer(), batch)
		if err != nil {
			i.err = errors.Wrap(err, "resolve messages")
			return false
		}

		if len(gone) > 0 {
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
	if !ok {
		i.markSkipped(ctx, []int{id})
		return false
	}

	ext := strings.ToLower(filepath.Ext(item.Name))
	if len(i.include) > 0 {
		if _, ok := i.include[ext]; !ok {
			i.markDone(id)
			return false
		}
	}
	if len(i.exclude) > 0 {
		if _, ok := i.exclude[ext]; ok {
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

	path := filepath.Join(i.dir, name)
	if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() && stat.Size() == item.Size {
		// the iterator only sees ids that passed the pre-filter in Runner.missing,
		// so this is just a safety net for files that appeared meanwhile
		i.markDone(id)
		return false
	}

	e := newElem(from, id, mediaFile{item, peerID(from), id}, item.Date, path)
	if err = e.start(i.opts.takeout); err != nil {
		i.err = errors.Wrap(err, "create file")
		return false
	}

	select {
	case <-ctx.Done():
		_ = e.closeFile()
		i.err = ctx.Err()
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
func (i *iter) Drain() {
	for {
		select {
		case e := <-i.elems:
			_ = e.(*elem).closeFile()
		default:
			close(i.elems)
			return
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
	i.mu.Lock()
	i.finished[id] = struct{}{}
	i.mu.Unlock()

	if i.opts.onFinish != nil {
		i.opts.onFinish([]int{id})
	}
}

// markSkipped records ids that have no media, e.g. deleted messages.
func (i *iter) markSkipped(ctx context.Context, ids []int) {
	i.mu.Lock()
	for _, id := range ids {
		i.finished[id] = struct{}{}
	}
	i.mu.Unlock()

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
	i.mu.Lock()
	defer i.mu.Unlock()

	_, ok := i.finished[id]
	return ok
}

func peerID(p peers.Peer) int64 { return p.ID() }
