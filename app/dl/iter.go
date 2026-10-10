package dl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/atomic"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/diagnostic"
	"github.com/iyear/tdl/core/downloader"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/internal/transfer"
	"github.com/iyear/tdl/pkg/filterMap"
	"github.com/iyear/tdl/pkg/tmessage"
	"github.com/iyear/tdl/pkg/tplfunc"
	"github.com/iyear/tdl/pkg/utils"
)

const tempExt = ".tmp"

type fileTemplate struct {
	DialogID     int64
	MessageID    int
	GroupDir     string
	MessageDate  int64
	FileName     string
	FileCaption  string
	FileSize     string
	DownloadDate int64
}

type iter struct {
	pool    dcpool.Pool
	manager *peers.Manager
	dialogs []*tmessage.Dialog
	tpl     *template.Template
	include map[string]struct{}
	exclude map[string]struct{}
	opts    Options
	delay   time.Duration

	mu                *sync.Mutex
	finished          map[int]struct{}
	completed         map[string]completion
	selectedKeys      map[string]struct{}
	reservations      *transfer.Reservations
	fingerprint       string
	legacyFingerprint string
	// This param is kept for potential future use but is currently unused.
	// preSum       []int
	logicalPos   int // logical position for finished tracking
	dialogIndex  int // physical position: current dialog in dialogs array
	messageIndex int // physical position: current message in dialog.Messages array

	// TODO(Hexa): counter is de facto not be used in the codebase, but I perfer to reserve it. The key point is whether it still needs to be atomic or not.
	counter        *atomic.Int64
	skippedDeleted *atomic.Int64 // count of skipped deleted messages
	deletedIDs     []string      // IDs of deleted messages (format: "dialogID/messageID")
	elem           chan downloader.Elem
	err            error
}

func newIter(pool dcpool.Pool, manager *peers.Manager, dialog [][]*tmessage.Dialog,
	opts Options, delay time.Duration,
) (*iter, error) {
	tpl, err := template.New("dl").
		Funcs(tplfunc.FuncMap(tplfunc.All...)).
		Parse(opts.Template)
	if err != nil {
		return nil, diagnostic.Describe(errors.Wrap(err, "parse template"), corei18n.Message{ID: "errors.context.parse_template", Args: map[string]any{"Reason": err}})
	}

	dialogs := flatDialogs(dialog)
	// if msgs is empty, return error to avoid range out of index
	if len(dialogs) == 0 {
		return nil, diagnostic.Describe(errors.Errorf("you must specify at least one message"), corei18n.Message{ID: "errors.message.you_must_specify_at_least_one_message"})
	}

	// include and exclude
	normalizeExt := func(ext string) string { return strings.ToLower(fsutil.AddPrefixDot(ext)) }
	includeMap := filterMap.New(opts.Include, normalizeExt)
	excludeMap := filterMap.New(opts.Exclude, normalizeExt)

	// to keep fingerprint stable
	sortDialogs(dialogs, opts.Desc)
	root, err := transfer.CanonicalPath(opts.Dir)
	if err != nil {
		return nil, err
	}
	opts.Dir = root
	reservations := opts.Reservations
	if reservations == nil {
		reservations = &transfer.Reservations{}
	}

	return &iter{
		pool:    pool,
		manager: manager,
		dialogs: dialogs,
		opts:    opts,
		include: includeMap,
		exclude: excludeMap,
		tpl:     tpl,
		delay:   delay,

		mu:                &sync.Mutex{},
		finished:          make(map[int]struct{}),
		fingerprint:       downloadFingerprint(dialogs, opts),
		legacyFingerprint: fingerprint(dialogs),
		completed:         make(map[string]completion),
		selectedKeys:      make(map[string]struct{}),
		reservations:      reservations,
		// This param is kept for potential future use but is currently unused.
		// preSum:       preSum(dialogs),
		logicalPos:     0,
		dialogIndex:    0,
		messageIndex:   0,
		counter:        atomic.NewInt64(-1),
		skippedDeleted: atomic.NewInt64(0),
		deletedIDs:     make([]string, 0),
		elem:           make(chan downloader.Elem, 10), // grouped message buffer
		err:            nil,
	}, nil
}

func (i *iter) Next(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		i.err = ctx.Err()
		return false
	default:
	}

	// if delay is set, sleep for a while for each iteration
	if i.delay > 0 && (i.dialogIndex+i.messageIndex) > 0 { // skip first delay
		timer := time.NewTimer(i.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			i.err = ctx.Err()
			return false
		case <-timer.C:
		}
	}

	if len(i.elem) > 0 { // there are messages(grouped) in channel that not processed
		return true
	}

	for {
		ok, skip := i.process(ctx)
		if skip {
			continue
		}

		return ok
	}
}

func (i *iter) process(ctx context.Context) (ret bool, skip bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	// end of iteration or error occurred
	if i.dialogIndex >= len(i.dialogs) || i.messageIndex >= len(i.dialogs[i.dialogIndex].Messages) || i.err != nil {
		return false, false
	}

	peer, msg := i.dialogs[i.dialogIndex].Peer, i.dialogs[i.dialogIndex].Messages[i.messageIndex]

	// Record current logical position before processing
	startLogicalPos := i.logicalPos

	// Defer physical position increment
	defer func() {
		if i.messageIndex++; i.dialogIndex < len(i.dialogs) && i.messageIndex >= len(i.dialogs[i.dialogIndex].Messages) {
			i.dialogIndex++
			i.messageIndex = 0
		}
	}()

	from, err := i.manager.FromInputPeer(ctx, peer)
	if err != nil {
		i.err = diagnostic.Describe(errors.Wrap(err, "resolve from input peer"), corei18n.Message{ID: "errors.context.resolve_from_input_peer", Args: map[string]any{"Reason": err}})
		return false, false
	}
	message, err := tutil.GetSingleMessage(ctx, i.pool.Default(ctx), peer, msg)
	if err != nil {
		// Check if the error is due to a deleted message
		if errors.Is(err, tutil.ErrMessageDeleted) {
			logctx.From(ctx).Info("Message may be deleted, skipping",
				zap.Int64("dialog_id", tutil.GetInputPeerID(peer)),
				zap.Int("message_id", msg),
			)
			i.skippedDeleted.Inc()                                                                     // increment skipped deleted counter
			i.deletedIDs = append(i.deletedIDs, fmt.Sprintf("%d/%d", tutil.GetInputPeerID(peer), msg)) // track deleted message ID
			i.logicalPos++                                                                             // increment logical position for skipped message
			return false, true
		}
		i.err = diagnostic.Describe(errors.Wrap(err, "resolve message"), corei18n.Message{ID: "errors.context.resolve_message", Args: map[string]any{"Reason": err}})
		return false, false
	}

	if _, ok := message.GetGroupedID(); ok && i.opts.Group {
		return i.processGrouped(ctx, message, from, startLogicalPos)
	}

	ret, skip = i.processSingle(ctx, message, from, startLogicalPos)
	i.logicalPos++ // increment logical position after processing
	return ret, skip
}

func (i *iter) processSingle(ctx context.Context, message *tg.Message, from peers.Peer, logicalPos int) (bool, bool) {
	item, ok := tmedia.GetMedia(message)
	if !ok {
		logctx.From(ctx).Warn("Message has no media",
			zap.Int64("dialog_id", from.ID()),
			zap.Int("message_id", message.ID),
		)

		return false, true
	}

	// process include and exclude
	ext := strings.ToLower(filepath.Ext(item.Name))
	if _, ok = i.include[ext]; len(i.include) > 0 && !ok {
		return false, true
	}
	if _, ok = i.exclude[ext]; len(i.exclude) > 0 && ok {
		return false, true
	}

	toName := bytes.Buffer{}
	err := i.tpl.Execute(&toName, &fileTemplate{
		DialogID:     from.ID(),
		MessageID:    message.ID,
		GroupDir:     i.opts.GroupDirByMessage[message.ID],
		MessageDate:  int64(message.Date),
		FileName:     item.Name,
		FileCaption:  message.Message,
		FileSize:     utils.Byte.FormatBinaryBytes(item.Size),
		DownloadDate: time.Now().Unix(),
	})
	if err != nil {
		i.err = diagnostic.Describe(errors.Wrap(err, "execute template"), corei18n.Message{ID: "errors.context.execute_template", Args: map[string]any{"Reason": err}})
		return false, false
	}
	finalPath, err := fsutil.JoinWithin(i.opts.Dir, toName.String())
	if err != nil {
		i.err = diagnostic.Describe(errors.Wrap(err, "resolve output path"), corei18n.Message{ID: "errors.context.resolve_output_path", Args: map[string]any{"Reason": err}})
		return false, false
	}
	identity, err := downloader.FileIdentityOf(mediaDownloadFile{item})
	if err != nil {
		i.err = err
		return false, false
	}
	encoded, _ := json.Marshal(struct {
		Peer     string
		Message  int
		Identity downloader.FileIdentity
		Path     string
	}{from.InputPeer().TypeName() + ":" + fmt.Sprint(from.ID()), message.ID, identity, finalPath})
	resumeKey := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if _, seen := i.selectedKeys[resumeKey]; seen {
		return false, true
	}
	i.selectedKeys[resumeKey] = struct{}{}
	finalPath, err = i.reservations.Reserve(finalPath, resumeKey)
	if err != nil {
		i.err = err
		return false, false
	}
	if saved, ok := i.completed[resumeKey]; ok {
		if stat, err := os.Stat(saved.Path); err == nil && saved.Target == finalPath && saved.Size == item.Size && stat.Mode().IsRegular() && stat.Size() == item.Size && stat.ModTime().UnixNano() == saved.ModTime {
			if saved.Path != finalPath {
				if _, err := i.reservations.Reserve(saved.Path, resumeKey); err != nil {
					i.err = err
					return false, false
				}
			}
			return false, true
		}
	}

	if i.opts.SkipSame {
		if stat, err := os.Stat(finalPath); err == nil {
			if fsutil.GetNameWithoutExt(toName.String()) == fsutil.GetNameWithoutExt(stat.Name()) &&
				stat.Mode().IsRegular() && stat.Size() == item.Size {
				return false, true
			}
		}
	}

	path := fmt.Sprintf("%s%s", finalPath, tempExt)

	// #113. If path contains dirs, create it. So now we support nested dirs.
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		i.err = diagnostic.Describe(errors.Wrap(err, "create dir"), corei18n.Message{ID: "errors.context.create_dir", Args: map[string]any{"Reason": err}})
		return false, false
	}

	to, parts, err := downloader.OpenPartialFile(path, mediaDownloadFile{item})
	if err != nil {
		i.err = diagnostic.Describe(errors.Wrap(err, "create file"), corei18n.Message{ID: "errors.context.create_file", Args: map[string]any{"Reason": err}})
		return false, false
	}
	for _, recoveryPath := range parts.RecoveryPaths() {
		logctx.From(ctx).Warn("Preserved unverified partial download", zap.String("path", recoveryPath))
	}

	i.elem <- &iterElem{
		id:         int(i.counter.Inc()),
		logicalPos: logicalPos,

		from:    from,
		fromMsg: message,
		file:    item,

		to:    to,
		parts: parts,

		opts:      i.opts,
		finalPath: finalPath, requestedPath: finalPath, resumeKey: resumeKey, reservations: i.reservations,
	}

	return true, false
}

func (i *iter) processGrouped(ctx context.Context, message *tg.Message, from peers.Peer, startLogicalPos int) (bool, bool) {
	grouped, err := tutil.GetGroupedMessages(ctx, i.pool.Default(ctx), from.InputPeer(), message)
	if err != nil {
		i.err = diagnostic.Describe(errors.Wrapf(err, "resolve grouped message %d/%d", from.ID(), message.ID), corei18n.Message{ID: "errors.context.resolve_grouped_message_value_value", Args: map[string]any{"Arg1": from.ID(), "Arg2": message.ID, "Reason": err}})
		return false, false
	}

	hasValid := false

	for idx, msg := range grouped {
		logicalPos := startLogicalPos + idx

		ret, skip := i.processSingle(ctx, msg, from, logicalPos)

		// if processSingle encounters a fatal error (not just skip), propagate it
		if !ret && !skip {
			// i.err should already be set by processSingle
			return false, false
		}

		if ret {
			hasValid = true
		}
	}

	// increment logical position by the number of messages in the group
	i.logicalPos += len(grouped)

	return hasValid, !hasValid
}

func (i *iter) Value() downloader.Elem {
	return <-i.elem
}

func (i *iter) Err() error {
	return i.err
}

// Drain closes album members prepared before a later member failed, or before
// cancellation prevented Value from consuming the queue. Call after workers
// settle; these members never reached Progress.OnAdd.
func (i *iter) Drain() error {
	var err error
	for {
		select {
		case elem := <-i.elem:
			err = multierr.Append(err, elem.(*iterElem).Finalize(context.Canceled))
		default:
			return err
		}
	}
}

func (i *iter) SetFinished(finished map[int]struct{}) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.finished = finished
}

func (i *iter) Finished() map[int]struct{} {
	i.mu.Lock()
	defer i.mu.Unlock()

	return maps.Clone(i.finished)
}

func (i *iter) Fingerprint() string {
	return i.fingerprint
}

func (i *iter) Finish(id int) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.finished[id] = struct{}{}
}

type mediaDownloadFile struct{ media *tmedia.Media }

func (f mediaDownloadFile) Location() tg.InputFileLocationClass { return f.media.InputFileLoc }
func (f mediaDownloadFile) Size() int64                         { return f.media.Size }
func (f mediaDownloadFile) DC() int                             { return f.media.DC }

type completion struct {
	Path    string `json:"path"`
	Target  string `json:"target"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time_ns"`
}

func (i *iter) Complete(e *iterElem) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.completed == nil {
		i.completed = make(map[string]completion)
	}
	if e.resumeKey != "" {
		if stat, err := os.Stat(e.finalPath); err == nil && stat.Mode().IsRegular() && stat.Size() == e.Size() {
			i.completed[e.resumeKey] = completion{Path: e.finalPath, Target: e.requestedPath, Size: e.Size(), ModTime: stat.ModTime().UnixNano()}
		}
	}
}

func (i *iter) Completed() map[string]completion {
	i.mu.Lock()
	defer i.mu.Unlock()
	return maps.Clone(i.completed)
}

func downloadFingerprint(dialogs []*tmessage.Dialog, opts Options) string {
	type source struct {
		Kind     string
		ID       int64
		Messages []int
	}
	sources := make([]source, 0, len(dialogs))
	for _, dialog := range dialogs {
		sources = append(sources, source{dialog.Peer.TypeName(), tutil.GetInputPeerID(dialog.Peer), dialog.Messages})
	}
	b, _ := json.Marshal(struct {
		Version          int
		Sources          []source
		Dir, Template    string
		Include, Exclude []string
		Group, Rewrite   bool
		Dirs             map[int]string
	}{2, sources, opts.Dir, opts.Template, opts.Include, opts.Exclude, opts.Group, opts.RewriteExt, opts.GroupDirByMessage})
	return fmt.Sprintf("v2-%x", sha256.Sum256(b))
}

func (i *iter) Total() int {
	i.mu.Lock()
	defer i.mu.Unlock()

	total := 0
	for _, m := range i.dialogs {
		total += len(m.Messages)
	}
	return total
}

func (i *iter) SkippedDeleted() int64 {
	return i.skippedDeleted.Load()
}

func (i *iter) DeletedIDs() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.deletedIDs
}

// positionToLogicalIndex converts physical position (dialogIndex, messageIndex) to logical index
// This method is kept for potential future use but is currently unused.
// func (i *iter) positionToLogicalIndex(dialogIdx, messageIdx int) int {
// 	return i.preSum[dialogIdx] + messageIdx
// }

func flatDialogs(dialogs [][]*tmessage.Dialog) []*tmessage.Dialog {
	res := make([]*tmessage.Dialog, 0)
	for _, d := range dialogs {
		for _, dialog := range d {
			if dialog != nil && len(dialog.Messages) > 0 {
				res = append(res, dialog)
			}
		}
	}
	return res
}

func sortDialogs(dialogs []*tmessage.Dialog, desc bool) {
	sort.Slice(dialogs, func(i, j int) bool {
		return tutil.GetInputPeerID(dialogs[i].Peer) <
			tutil.GetInputPeerID(dialogs[j].Peer) // increasing order
	})

	for _, m := range dialogs {
		sort.Slice(m.Messages, func(i, j int) bool {
			if desc {
				return m.Messages[i] > m.Messages[j]
			}
			return m.Messages[i] < m.Messages[j]
		})
	}
}

// preSum of dialogs
// This method is kept for potential future use but is currently unused.
// func preSum(dialogs []*tmessage.Dialog) []int {
// 	sum := make([]int, len(dialogs)+1)
// 	for i, m := range dialogs {
// 		sum[i+1] = sum[i] + len(m.Messages)
// 	}
// 	return sum
// }

func fingerprint(dialogs []*tmessage.Dialog) string {
	endian := binary.BigEndian
	buf, b := &bytes.Buffer{}, make([]byte, 8)
	for _, m := range dialogs {
		endian.PutUint64(b, uint64(tutil.GetInputPeerID(m.Peer)))
		buf.Write(b)
		for _, msg := range m.Messages {
			endian.PutUint64(b, uint64(msg))
			buf.Write(b)
		}
	}

	return fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
}
