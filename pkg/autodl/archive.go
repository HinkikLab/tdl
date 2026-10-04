package autodl

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/iyear/tdl/app/chat"
)

// runArchiveWindow applies the same range/incremental precedence as ordinary
// batch jobs. Archive manifests still decide whether each file is complete.
func (r *Runner) runArchiveWindow(ctx context.Context, job *Job, dir string, download func(chat.ArchiveWindow) error) error {
	if !job.UsesIncremental(r.cfg.Incremental) {
		return download(chat.ArchiveWindow{StartID: num(job.StartComment), EndID: num(job.EndComment)})
	}
	scope, err := r.archiveStateScope(job, dir)
	if err != nil {
		return err
	}
	store, state, err := LoadStateStore(r.statePath(job), scope)
	if err != nil {
		return err
	}
	end := time.Now().Unix()
	overlap := pick(num(r.opts.OverlapSeconds), num(job.Overlap), num(r.cfg.OverlapSeconds), DefaultOverlapSeconds)
	start := max(int64(0), state.GetLastTS()-int64(overlap))
	color.Cyan("Incremental archive window: %s ~ %s (last=%s, overlap=%ds)",
		time.Unix(start, 0).Format(time.RFC3339), time.Unix(end, 0).Format(time.RFC3339),
		formatLastTS(state.GetLastTS()), overlap)
	if err := download(chat.ArchiveWindow{Since: start, Until: end}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.opts.CheckOnly {
		return nil
	}
	return r.advanceIncremental(ctx, job, store, state, nil, end)
}

func (r *Runner) archiveStateScope(job *Job, dir string) (string, error) {
	link := Link{}
	if job.FollowLinks {
		var err error
		link, err = ParseLink(job.ChatURL)
		if err != nil {
			return "", err
		}
	} else {
		target, err := chat.ParseTagTarget(job.ChatURL, num(job.TopicID))
		if err != nil {
			return "", err
		}
		link.Chat, link.MessageID = target.Chat, target.TopicID
	}
	// Include selection and output semantics so changing tags or link options
	// cannot inherit a timestamp that would hide previously unselected posts.
	data, err := json.Marshal(struct {
		Base   string
		PostID int
		Follow bool
		Tag    string
		Tags   []string
		Match  string
		Links  chat.LinkOptions
	}{r.stateScope(job, link, dir), link.MessageID, job.FollowLinks,
		strings.ToLower(job.Tag), job.Tags, job.TagMatch, job.LinkOptions})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("archive-v1|%x", sha256.Sum256(data)), nil
}
