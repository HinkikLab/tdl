package autodl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/peers"
	"go.uber.org/zap"

	"github.com/iyear/tdl/app/chat"
	"github.com/iyear/tdl/core/logctx"
)

// runJob processes a single config job.
func (r *Runner) runJob(ctx context.Context, job *Job, threads, limit int, onResolved func(string)) error {
	log := logctx.From(ctx)

	dir := job.Dir()
	if r.opts.Dir != "" {
		dir = filepath.Join(r.opts.Dir, job.Subdir)
	}
	writeMetadata := job.WritesMetadata(r.cfg.WriteMetadata)
	if job.FollowLinks {
		return r.runArchiveWindow(ctx, job, dir, func(window chat.ArchiveWindow) error {
			return chat.DownloadLinked(ctx, r.client, r.storage, chat.LinkedOptions{
				Chat: job.ChatURL, Dir: dir, Tag: job.Tag, Tags: job.Tags, TagMatch: job.TagMatch,
				Window: window, MaxPosts: job.MaxPosts, CheckOnly: r.opts.CheckOnly,
				Takeout: r.opts.Takeout, Threads: threads, Limit: limit, Pool: r.pool, Links: job.LinkOptions,
				Include: r.opts.Include, Exclude: r.opts.Exclude,
				BotUpdates:      r.opts.BotUpdates,
				Unavailable:     r.unavailable,
				WriteMetadata:   &writeMetadata,
				OnResolved:      onResolved,
				Manager:         r.manager,
				Delay:           r.opts.Delay,
				Reservations:    &r.reservations,
				Account:         r.account,
				AccountVerified: true,
				OnResult:        r.observeCounts,
			})
		})
	}
	if job.IsTagJob() {
		return r.runArchiveWindow(ctx, job, dir, func(window chat.ArchiveWindow) error {
			return chat.DownloadTag(ctx, r.client, r.storage, chat.TagOptions{
				Chat: job.ChatURL, Tag: job.Tag, Tags: job.Tags,
				TopicID:  num(job.TopicID),
				TagMatch: job.TagMatch, Dir: dir,
				CheckOnly: r.opts.CheckOnly, Takeout: r.opts.Takeout,
				Threads: threads, Limit: limit, PoolSize: r.poolSize,
				PoolSizeSet:     true,
				Pool:            r.pool,
				MaxPosts:        job.MaxPosts,
				Window:          window,
				WriteMetadata:   &writeMetadata,
				OnResolved:      onResolved,
				Manager:         r.manager,
				Delay:           r.opts.Delay,
				Reservations:    &r.reservations,
				Account:         r.account,
				AccountVerified: true,
				OnResult:        r.observeCounts,
			})
		})
	}
	link, err := ParseLink(job.ChatURL)
	if err != nil {
		return err
	}
	incremental := job.UsesIncremental(r.cfg.Incremental)
	var peer peers.Peer
	if incremental {
		peer, err = r.scanPeer(ctx, job, link)
	} else {
		peer, err = r.resolveDialog(ctx, job, link)
	}
	if err != nil {
		return err
	}
	topicID, topicTitle := 0, ""
	if incremental {
		topicID = num(job.TopicID)
	}
	if topicID > 0 {
		if topic, err := chat.ResolveForumTopic(ctx, r.pool.Default(ctx), peer.InputPeer(), topicID); err == nil {
			topicTitle = topic.Title
		} else {
			return errors.Wrapf(err, "resolve topic_id %d", topicID)
		}
	}
	if onResolved != nil {
		onResolved(chat.TargetName(peer, topicID, topicTitle, !incremental || (topicID == 0 && job.ReplyPostID == nil)))
	}

	if !r.opts.CheckOnly {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return errors.Wrapf(err, "create download dir %s", dir)
		}
	}

	statePath := r.statePath(job)
	store, state, err := LoadStateStore(statePath, r.peerStateScope(job, link, dir, peer))
	if err != nil {
		return err
	}

	if r.opts.RetrySkipped {
		if n := state.ClearSkipped(); n > 0 {
			color.Yellow("Retrying %d message(s) that an earlier run recorded as unavailable", n)
			log.Info("Clear skipped messages", zap.Int("count", n), zap.String("state", statePath))

			if !r.opts.CheckOnly {
				if serr := store.Save(); serr != nil {
					return serr
				}
			}
		}
	}

	log.Info("Start job",
		zap.String("chat_url", job.ChatURL),
		zap.String("dir", dir),
		zap.Bool("incremental", incremental),
		zap.Bool("comment", job.CommentMode()),
		zap.String("state", statePath))

	if !incremental {
		return r.runRangeWindow(ctx, job, link, dir, store, state, threads, limit)
	}

	var targets []int
	var exportTS int64
	defer func() { r.setTargets(len(targets)) }()

	if incremental {
		targets, exportTS, err = r.planIncremental(ctx, job, link, dir, state)
		if err != nil {
			return err
		}
	}

	if len(targets) == 0 {
		log.Info("Nothing to download")
		color.Yellow("No messages to download for this job")

		// an empty window still has to move the timestamp, otherwise the next
		// run scans the same range again
		if incremental && !r.opts.CheckOnly {
			return r.advanceIncremental(ctx, job, store, state, nil, exportTS)
		}
		return nil
	}

	missing := r.missing(targets, state)

	log.Info("Plan",
		zap.Int("targets", len(targets)),
		zap.Int("finished", state.Len()),
		zap.Int("missing", len(missing)))

	color.Cyan("Target range: %s", formatIDs(targets))
	color.Cyan("Recorded as finished: %d, to validate/download: %d", state.Len(), len(missing))

	if r.opts.CheckOnly {
		color.Yellow("Check only: skipping the download step")
		return nil
	}

	if len(missing) == 0 {
		color.Green("Everything is already downloaded")
		if incremental {
			return r.advanceIncremental(ctx, job, store, state, targets, exportTS)
		}
		return nil
	}

	if err = r.download(ctx, job, link, dir, missing, store, state, threads, limit); err != nil {
		return err
	}

	left := state.Missing(targets)
	if len(left) > 0 {
		color.Yellow("%d message(s) are still missing after this run", len(left))
		log.Warn("Messages still missing", zap.Int("count", len(left)), zap.Ints("ids", left))

		if !r.confirm(ctx, fmt.Sprintf("Retry the %d missing message(s) now?", len(left)), true) {
			if incremental {
				color.Yellow("Keeping the incremental timestamp so the next run covers this range again")
			}
			return errors.Errorf("%d message(s) still missing", len(left))
		}

		if err = r.download(ctx, job, link, dir, left, store, state, threads, limit); err != nil {
			return err
		}

		if left = state.Missing(targets); len(left) > 0 {
			color.Yellow("%d message(s) are still missing", len(left))
			return errors.Errorf("%d message(s) still missing", len(left))
		}
	}

	if incremental {
		return r.advanceIncremental(ctx, job, store, state, targets, exportTS)
	}

	return nil
}
