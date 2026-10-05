package autodl

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/tmedia"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/texpr"
)

// tmpDirName is the directory that holds the export json files, mirroring the
// python script's download_dir/.tdl_tmp.
const tmpDirName = ".tdl_tmp"

// exportMessage is the subset of the export json the planner reads.
type exportMessage struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	File string `json:"file"`
	Date int    `json:"date,omitempty"`
	Text string `json:"text,omitempty"`
}

// exportFile is the top level structure written by tdl chat export.
type exportFile struct {
	ID       int64           `json:"id"`
	Messages []exportMessage `json:"messages"`
}

// planIncremental discovers the message ids of the current time window.
//
// It replaces the python "plan B" flow: export the chat by timestamp, read the
// message ids back from the json, then let the shared dedup logic decide what
// still has to be downloaded. Because tdl runs in process now, no external
// export process and no re-parsing of foreign JSON is involved.
//
// It returns the ids of the window and the timestamp the window ended at,
// which is the value the state may advance to once everything is downloaded.
func (r *Runner) planIncremental(ctx context.Context, job *Job, link Link, dir string, state *State) ([]int, int64, error) {
	log := logctx.From(ctx)

	dialog, err := r.scanPeer(ctx, job, link)
	if err != nil {
		return nil, 0, err
	}

	now := time.Now().Unix()
	overlap := r.overlapSeconds(job)

	start := int64(0)
	if state.GetLastTS() > 0 {
		start = state.GetLastTS() - int64(overlap)
		if start < 0 {
			start = 0
		}
	}

	log.Info("Incremental plan",
		zap.Int64("start_ts", start),
		zap.Int64("end_ts", now),
		zap.Int64("last_ts", state.GetLastTS()),
		zap.Int("overlap", overlap))

	color.Cyan("Incremental window: %s ~ %s (last=%s, overlap=%ds)",
		time.Unix(start, 0).Format(time.RFC3339),
		time.Unix(now, 0).Format(time.RFC3339),
		formatLastTS(state.GetLastTS()),
		overlap)

	ids, err := r.collectIDs(ctx, job, dialog, start, now, dir)
	if err != nil {
		return nil, 0, err
	}

	log.Info("Incremental window collected", zap.Int("messages", len(ids)))

	if len(ids) == 0 {
		color.Yellow("The incremental window holds no media messages")
	}

	return ids, now, nil
}

// collectIDs walks the history of the dialog inside [start, end] and returns
// the ids of the messages that carry media.
func (r *Runner) collectIDs(ctx context.Context, job *Job, dialog peers.Peer, start, end int64, dir string) ([]int, error) {
	api := r.pool.Default(ctx)

	// an export_filter is an expr expression evaluated for every message, the
	// same one the `tdl chat export -f` flag accepts
	var filter *vm.Program
	if f := strings.TrimSpace(job.ExportFilter); f != "" {
		compiled, err := expr.Compile(f, expr.AsBool())
		if err != nil {
			return nil, errors.Wrapf(err, "compile export_filter %q", f)
		}
		filter = compiled
	}

	// a reply_post_id means the ids live in the comment section of one post,
	// which is exactly what messages.getReplies returns
	q := query.NewQuery(api).Messages()
	var q2 messages.Query
	if job.ReplyPostID != nil && *job.ReplyPostID > 0 {
		q2 = q.GetReplies(dialog.InputPeer()).MsgID(*job.ReplyPostID)
	} else if num(job.TopicID) > 0 {
		q2 = q.GetReplies(dialog.InputPeer()).MsgID(*job.TopicID)
	} else {
		q2 = q.GetHistory(dialog.InputPeer())
	}

	it := messages.NewIterator(q2, 100)

	// #89: OffsetDate is inclusive of the boundary message
	it = it.OffsetDate(int(end) + 1)

	seen := make(map[int]struct{})
	out := make([]int, 0)
	var export *exportStream
	if !r.opts.CheckOnly {
		var err error
		export, err = newExportStream(dir, dialog.ID())
		if err != nil {
			logctx.From(ctx).Warn("Create export json", zap.Error(err))
		}
	}
	defer func() {
		if export != nil {
			export.Abort()
		}
	}()
	scanned := 0
	accept := func(m *tg.Message) error {
		if int64(m.Date) < start || int64(m.Date) > end {
			return nil
		}
		if scanned++; scanned > maxIncrementalScan {
			return errors.Errorf("incremental scan exceeded %d messages; keeping last_ts to avoid losing older messages", maxIncrementalScan)
		}

		if job.TopicID != nil && *job.TopicID > 0 {
			// topics are message threads; only keep messages that answer the
			// topic root. Direct replies often omit ReplyToTopID, in which
			// case ReplyToMsgID itself is the topic root.
			if m.ID != *job.TopicID {
				top, ok := m.GetReplyTo()
				if !ok {
					return nil
				}
				header, ok := top.(*tg.MessageReplyHeader)
				if !ok {
					return nil
				}
				topicID := header.ReplyToMsgID
				if id, ok := header.GetReplyToTopID(); ok {
					topicID = id
				}
				if topicID != *job.TopicID {
					return nil
				}
			}
		}

		media, hasMedia := tmedia.GetMedia(m)
		if !hasMedia && !job.ExportAll {
			return nil
		}

		if filter != nil {
			res, err := texpr.Run(filter, texpr.ConvertEnvMessage(m))
			if err != nil {
				return errors.Wrap(err, "run export_filter")
			}
			keep, _ := res.(bool)
			if !keep {
				return nil
			}
		}

		if _, ok := seen[m.ID]; ok {
			return nil
		}
		seen[m.ID] = struct{}{}
		out = append(out, m.ID)
		if r.opts.CheckOnly {
			return nil
		}

		name := ""
		if media != nil {
			name = media.Name
		}

		item := exportMessage{
			ID:   m.ID,
			Type: "message",
			File: name,
		}
		if job.WithContent {
			item.Date = m.Date
			item.Text = m.Message
		}
		if export != nil {
			if err := export.Add(item); err != nil {
				logctx.From(ctx).Warn("Write export json", zap.Error(err))
				export.Abort()
				export = nil
			}
		}
		return nil
	}
	// Include root media explicitly to preserve the previous topic selection
	// contract while querying replies on the server.
	if num(job.TopicID) > 0 && job.ReplyPostID == nil {
		found, _, err := tutil.GetMessages(ctx, api, dialog.InputPeer(), []int{*job.TopicID})
		if err != nil {
			return nil, errors.Wrap(err, "resolve topic root")
		}
		if root := found[*job.TopicID]; root != nil {
			if err := accept(root); err != nil {
				return nil, err
			}
		}
	}
	for it.Next(ctx) {
		m, ok := it.Value().Msg.(*tg.Message)
		if !ok {
			continue
		}
		if int64(m.Date) < start {
			break
		}
		if err := accept(m); err != nil {
			return nil, err
		}
	}

	if err := it.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate history")
	}

	sort.Ints(out)

	// Keep the export on disk for debugging, exactly like the python script
	// kept its export json files around.
	if export != nil {
		if err := export.Finalize(); err != nil {
			logctx.From(ctx).Warn("Write export json", zap.Error(err))
		}
	}

	return out, nil
}

// maxIncrementalScan bounds how many history messages one incremental window
// may look at, so a bad (or missing) timestamp cannot turn into an endless
// crawl of the whole chat.
const maxIncrementalScan = 100000

// advanceIncremental moves the job's timestamp forward once the window has
// been fully processed.
//
// The timestamp only advances when nothing is missing, so a failed download is
// retried by the next run instead of being skipped forever.
func (r *Runner) advanceIncremental(ctx context.Context, job *Job, store *stateStore, state *State, targets []int, endTS int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	log := logctx.From(ctx)

	missing := state.Missing(targets)
	if len(missing) > 0 {
		log.Warn("Incremental window still incomplete, keeping last_ts",
			zap.Int("missing", len(missing)))

		return errors.Errorf("%d message(s) still missing; keeping last_ts", len(missing))
	}

	if endTS <= state.GetLastTS() {
		return nil
	}

	previous := state.GetLastTS()
	if err := ctx.Err(); err != nil {
		return err
	}
	state.SetLastTS(endTS)
	if err := store.Save(); err != nil {
		state.SetLastTS(previous)
		return errors.Wrap(err, "save state")
	}

	log.Info("Incremental timestamp advanced", zap.Int64("last_ts", endTS))
	color.Green("last_ts advanced to %s", time.Unix(endTS, 0).Format(time.RFC3339))

	return nil
}

func formatLastTS(ts int64) string {
	if ts <= 0 {
		return "never"
	}

	return time.Unix(ts, 0).Format(time.RFC3339)
}
