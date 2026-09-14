package autodl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	overlap := pick(num(r.opts.OverlapSeconds), num(job.Overlap), num(r.cfg.OverlapSeconds), DefaultOverlapSeconds)

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

	ids, _, err := r.collectIDs(ctx, job, dialog, start, now, dir)
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
//
// The first return value holds the message ids, the second the timestamps that
// were seen.
func (r *Runner) collectIDs(ctx context.Context, job *Job, dialog peers.Peer, start, end int64, dir string) ([]int, []int64, error) {
	api := r.pool.Default(ctx)

	// an export_filter is an expr expression evaluated for every message, the
	// same one the `tdl chat export -f` flag accepts
	var filter *vm.Program
	if f := strings.TrimSpace(job.ExportFilter); f != "" {
		compiled, err := expr.Compile(f, expr.AsBool())
		if err != nil {
			return nil, nil, errors.Wrapf(err, "compile export_filter %q", f)
		}
		filter = compiled
	}

	// a reply_post_id means the ids live in the comment section of one post,
	// which is exactly what messages.getReplies returns
	q := query.NewQuery(api).Messages()
	var q2 messages.Query
	if job.ReplyPostID != nil && *job.ReplyPostID > 0 {
		q2 = q.GetReplies(dialog.InputPeer()).MsgID(*job.ReplyPostID)
	} else {
		q2 = q.GetHistory(dialog.InputPeer())
	}

	it := messages.NewIterator(q2, 100)

	// #89: OffsetDate is inclusive of the boundary message
	it = it.OffsetDate(int(end) + 1)

	seen := make(map[int]struct{})
	out := make([]int, 0)
	dates := make([]int64, 0, 64)
	exported := make([]exportMessage, 0)
	scanned := 0

loop:
	for it.Next(ctx) {
		msg := it.Value()
		m, ok := msg.Msg.(*tg.Message)
		if !ok {
			continue
		}

		if m.Date < int(start) {
			break loop
		}

		if scanned++; scanned > maxIncrementalScan {
			logctx.From(ctx).Warn("Incremental window scan limit reached; the window is probably too large",
				zap.Int("limit", maxIncrementalScan))
			break loop
		}

		if job.TopicID != nil && *job.TopicID > 0 {
			// topics are message threads; only keep messages that answer the
			// topic root
			top, ok := m.GetReplyTo()
			if !ok {
				continue
			}
			header, ok := top.(*tg.MessageReplyHeader)
			if !ok || header.ReplyToTopID != *job.TopicID {
				continue
			}
		}

		media, hasMedia := tmedia.GetMedia(m)
		if !hasMedia && !job.ExportAll {
			continue
		}

		if filter != nil {
			res, err := texpr.Run(filter, texpr.ConvertEnvMessage(m))
			if err != nil {
				return nil, nil, errors.Wrap(err, "run export_filter")
			}
			keep, _ := res.(bool)
			if !keep {
				continue
			}
		}

		if _, ok := seen[m.ID]; ok {
			continue
		}
		seen[m.ID] = struct{}{}
		out = append(out, m.ID)
		dates = append(dates, int64(m.Date))

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
		exported = append(exported, item)
	}

	if err := it.Err(); err != nil {
		return nil, nil, errors.Wrap(err, "iterate history")
	}

	sort.Ints(out)

	// Keep the export on disk for debugging, exactly like the python script
	// kept its export json files around.
	if err := writeExport(dir, dialog, exported); err != nil {
		logctx.From(ctx).Warn("Write export json", zap.Error(err))
	}

	return out, dates, nil
}

// maxIncrementalScan bounds how many history messages one incremental window
// may look at, so a bad (or missing) timestamp cannot turn into an endless
// crawl of the whole chat.
const maxIncrementalScan = 100000

// writeExport stores the exported window under <dir>/.tdl_tmp so it can be
// inspected afterwards.
func writeExport(dir string, dialog peers.Peer, msgs []exportMessage) error {
	tmp := filepath.Join(dir, tmpDirName)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}

	name := fmt.Sprintf("tdl-export-%d-%d.json", dialog.ID(), time.Now().Unix())
	path := filepath.Join(tmp, name)

	b, err := json.MarshalIndent(exportFile{ID: dialog.ID(), Messages: msgs}, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, b, 0o644)
}

// advanceIncremental moves the job's timestamp forward once the window has
// been fully processed.
//
// The timestamp only advances when nothing is missing, so a failed download is
// retried by the next run instead of being skipped forever.
func (r *Runner) advanceIncremental(ctx context.Context, job *Job, store *stateStore, state *State, targets []int, endTS int64) error {
	log := logctx.From(ctx)

	missing := state.Missing(targets)
	if len(missing) > 0 {
		log.Warn("Incremental window still incomplete, keeping last_ts",
			zap.Int("missing", len(missing)))

		if !r.confirm(ctx, "Still missing messages. Advance the timestamp anyway (they may be skipped later)?", false) {
			color.Yellow("Keeping last_ts so the next run covers this window again")
			return nil
		}
	}

	if endTS <= state.GetLastTS() {
		return nil
	}

	state.SetLastTS(endTS)
	if err := store.Save(); err != nil {
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
