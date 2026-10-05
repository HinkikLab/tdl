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
	"github.com/iyear/tdl/core/util/tutil"
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
	if r.manager != nil {
		link, err := archiveLink(job)
		if err != nil {
			return err
		}
		peer, err := tutil.GetInputPeer(ctx, r.manager, link.Chat)
		if err != nil {
			return err
		}
		scope, err = r.archiveStateScopeForSource(job, dir, peerKey(peer))
		if err != nil {
			return err
		}
	}
	store, state, err := LoadStateStore(r.statePath(job), scope)
	if err != nil {
		return err
	}
	end := time.Now().Unix()
	overlap := r.overlapSeconds(job)
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
	return r.archiveStateScopeForSource(job, dir, "")
}

func archiveLink(job *Job) (Link, error) {
	link := Link{}
	if job.FollowLinks {
		var err error
		link, err = ParseLink(job.ChatURL)
		if err != nil {
			return link, err
		}
	} else {
		target, err := chat.ParseTagTarget(job.ChatURL, num(job.TopicID))
		if err != nil {
			return link, err
		}
		link.Chat, link.MessageID = target.Chat, target.TopicID
	}
	return link, nil
}

func (r *Runner) archiveStateScopeForSource(job *Job, dir, source string) (string, error) {
	link, err := archiveLink(job)
	if err != nil {
		return "", err
	}
	if source != "" {
		link.Chat = source
	}
	tags := normalizedTags(job)
	copyJob := *job
	copyJob.Chat = ""
	copyJob.ExportFilter = ""
	copyJob.ExportAll = false
	identityRunner := &Runner{opts: r.opts, cfg: r.cfg, account: r.account}
	identityRunner.opts.Template = ""
	if !job.FollowLinks {
		identityRunner.opts.Include = nil
		identityRunner.opts.Exclude = nil
	}
	match := job.TagMatch
	if match == "" {
		match = "any"
	}
	// Include selection and output semantics so changing tags or link options
	// cannot inherit a timestamp that would hide previously unselected posts.
	data, err := json.Marshal(struct {
		Base     string
		PostID   int
		Follow   bool
		Tags     []string
		Match    string
		MaxPosts int
		Metadata bool
		Links    any
	}{identityRunner.stateScope(&copyJob, link, dir), link.MessageID, job.FollowLinks,
		tags, match, job.MaxPosts, job.WritesMetadata(r.cfg.WriteMetadata), struct {
			Depth, Links, Messages, TopicMessages, Comments int
			ScanComments, Previews                          bool
		}{job.LinkOptions.MaxDepth, job.LinkOptions.MaxLinks, job.LinkOptions.MaxBotMessages, job.LinkOptions.MaxTopicMessages, job.LinkOptions.CommentLimit,
			job.LinkOptions.ScanComments == nil || *job.LinkOptions.ScanComments, job.LinkOptions.IncludePreviews == nil || *job.LinkOptions.IncludePreviews}})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("archive-v2|%x", sha256.Sum256(data)), nil
}

func normalizedTags(job *Job) []string {
	tags := append([]string{job.Tag}, job.Tags...)
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		tag = strings.TrimLeft(tag, "#")
		if tag != "" && !seen[tag] {
			out = append(out, tag)
			seen[tag] = true
		}
	}
	return out
}

func (r *Runner) overlapSeconds(job *Job) int {
	return pick(num(r.opts.OverlapSeconds), num(job.Overlap), num(r.cfg.OverlapSeconds), DefaultOverlapSeconds)
}
