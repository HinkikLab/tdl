package autodl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/core/dcpool"
	tdl "github.com/iyear/tdl/core/downloader"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/core/util/tutil"
	"github.com/iyear/tdl/pkg/prog"
	"github.com/iyear/tdl/pkg/utils"
)

// Options configures a batch run.
type Options struct {
	// ConfigPath is the config.json to read.
	ConfigPath string
	// DownloadOverrides replace the matching config values when set.
	Dir string
	// Template is the download name template. The tdl default is used when
	// empty.
	Template string
	// Include and Exclude filter file extensions.
	Include []string
	Exclude []string
	// Takeout enables takeout sessions, which lowers flood wait limits.
	Takeout bool

	// PoolSize, Threads and Limit override the config performance settings.
	PoolSize int
	Threads  int
	Limit    int
	// Delay is the delay between tasks.
	Delay time.Duration

	// CheckOnly only reports what would be downloaded.
	CheckOnly bool
	// Yes answers every confirmation with yes.
	Yes bool
	// Mode is "auto", "comment" or "direct".
	Mode string
	// Incremental forces incremental mode for every job.
	Incremental bool
	// StateFile forces the incremental state file.
	StateFile string
	// OverlapSeconds forces the incremental lookback window.
	OverlapSeconds *int
	// RetrySkipped forgets the messages that an earlier run recorded as
	// unavailable, so they are requested again.
	RetrySkipped bool

	// SkipResumePrompt resumes without asking.
	SkipResumePrompt bool

	// Middlewares are extra telegram middlewares.
	Middlewares []telegram.Middleware
}

// Runner executes a batch config against one authorized telegram client.
type Runner struct {
	opts Options
	cfg  *Config

	pool    dcpool.Pool
	manager *peers.Manager
}

// Run executes the batch download described by path.
//
// It is intentionally a drop-in replacement for python/run_unified.py: the
// same config.json, the same job semantics, but driven by tdl's own downloader
// so one process, one connection pool and batched requests are used for the
// whole run.
func Run(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts Options) error {
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return err
	}

	if opts.Incremental {
		for i := range cfg.Jobs {
			on := true
			cfg.Jobs[i].Incremental = &on
		}
	}

	return run(ctx, c, kvd, cfg, opts)
}

func run(ctx context.Context, c *telegram.Client, kvd storage.Storage, cfg *Config, opts Options) (rerr error) {
	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}

	switch strings.ToLower(opts.Mode) {
	case ModeAuto, ModeComment, ModeDirect:
	default:
		return errors.Errorf("invalid mode %q", opts.Mode)
	}

	for i := range cfg.Jobs {
		mode, err := cfg.Jobs[i].ResolveMode(opts.Mode)
		if err != nil {
			return errors.Wrapf(err, "job %d", i+1)
		}

		// the resolved mode decides which dialog the ids belong to, so it has
		// to be stored on the job rather than merely validated
		cfg.Jobs[i].mode = mode
	}

	poolSize := pick(opts.PoolSize, num(cfg.Pool), DefaultPoolSize)
	threads := pick(opts.Threads, num(cfg.Threads), DefaultThreads)
	limit := pick(opts.Limit, num(cfg.Limit), DefaultLimit)

	r := &Runner{opts: opts, cfg: cfg}
	r.pool = dcpool.NewPool(c, int64(poolSize),
		tclient.NewDefaultMiddlewares(ctx, 5*time.Minute)...)
	defer multierr.AppendInvoke(&rerr, multierr.Close(r.pool))

	r.manager = peers.Options{Storage: storage.NewPeers(kvd)}.Build(r.pool.Default(ctx))

	log := logctx.From(ctx)
	log.Info("Batch download",
		zap.String("config", opts.ConfigPath),
		zap.Int("jobs", len(cfg.Jobs)),
		zap.Int("pool", poolSize),
		zap.Int("threads", threads),
		zap.Int("limit", limit))

	color.Green("Batch download: %d job(s) from %s", len(cfg.Jobs), opts.ConfigPath)
	color.Cyan("Performance: pool=%d threads=%d limit=%d", poolSize, threads, limit)

	var failed int
	for idx := range cfg.Jobs {
		job := &cfg.Jobs[idx]

		jobCtx := logctx.With(ctx, log.Named(fmt.Sprintf("job%d", idx+1)))

		color.Blue("\n[%d/%d] %s", idx+1, len(cfg.Jobs), job.ChatURL)

		if err := r.runJob(jobCtx, job, threads, limit); err != nil {
			failed++
			log.Error("Job failed", zap.Int("job", idx+1), zap.Error(err))
			color.Red("Job %d failed: %s", idx+1, err)
		}
	}

	color.Green("\nBatch download finished: %d job(s), %d failed", len(cfg.Jobs), failed)
	if failed > 0 {
		return errors.Errorf("%d job(s) failed", failed)
	}

	return nil
}

// runJob processes a single config job.
func (r *Runner) runJob(ctx context.Context, job *Job, threads, limit int) error {
	log := logctx.From(ctx)

	link, err := ParseLink(job.ChatURL)
	if err != nil {
		return err
	}

	dir := job.Dir()
	if r.opts.Dir != "" {
		dir = filepath.Join(r.opts.Dir, job.Subdir)
	}

	if err = os.MkdirAll(dir, 0o755); err != nil {
		return errors.Wrapf(err, "create download dir %s", dir)
	}

	incremental := job.UsesIncremental(r.cfg.Incremental)

	statePath := r.statePath(job)
	store, state, err := LoadStateStore(statePath)
	if err != nil {
		return err
	}

	if r.opts.RetrySkipped {
		if n := state.ClearSkipped(); n > 0 {
			color.Yellow("Retrying %d message(s) that an earlier run recorded as unavailable", n)
			log.Info("Clear skipped messages", zap.Int("count", n), zap.String("state", statePath))

			if serr := store.Save(); serr != nil {
				return serr
			}
		}
	}

	log.Info("Start job",
		zap.String("chat_url", job.ChatURL),
		zap.String("dir", dir),
		zap.Bool("incremental", incremental),
		zap.Bool("comment", job.CommentMode()),
		zap.String("state", statePath))

	var targets []int
	var exportTS int64

	if incremental {
		targets, exportTS, err = r.planIncremental(ctx, job, link, dir, state)
		if err != nil {
			return err
		}
	} else {
		targets = rangeIDs(job)
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

	missing := r.missing(ctx, job, link, dir, targets, state, store)

	log.Info("Plan",
		zap.Int("targets", len(targets)),
		zap.Int("finished", state.Len()),
		zap.Int("missing", len(missing)))

	color.Cyan("Target range: %s", formatIDs(targets))
	color.Cyan("Already finished: %d, to download: %d", state.Len(), len(missing))

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
			return nil
		}

		if err = r.download(ctx, job, link, dir, left, store, state, threads, limit); err != nil {
			return err
		}

		if left = state.Missing(targets); len(left) > 0 {
			color.Yellow("%d message(s) are still missing", len(left))
		}
	}

	if incremental {
		return r.advanceIncremental(ctx, job, store, state, targets, exportTS)
	}

	return nil
}

// missing returns the targets that still have to be downloaded.
//
// An id counts as done when the state file records it, or when the file it
// would produce is already on disk. Checking the filesystem up front keeps the
// download iterator from re-resolving those messages one by one.
func (r *Runner) missing(ctx context.Context, job *Job, link Link, dir string, targets []int,
	state *State, store *stateStore) []int {
	out := make([]int, 0, len(targets))

	// without a resolvable dialog there is nothing to check on disk, so let
	// the download path report the error
	dialog, err := r.resolveDialog(ctx, job, link)
	if err != nil {
		logctx.From(ctx).Debug("Resolve dialog for the file check",
			zap.Error(err))

		return state.Missing(targets)
	}

	tpl, err := newNameTemplate(r.template())
	if err != nil {
		return state.Missing(targets)
	}

	// if the template uses anything but the dialog and message id there is no
	// way to predict the name before the message is resolved
	if !tpl.idsOnly() {
		return state.Missing(targets)
	}

	existing := existingNames(dir)
	found := 0
	for _, id := range targets {
		if state.IsFinished(id) {
			continue
		}

		if _, ok := existing[tpl.name(peerID(dialog), id)]; ok {
			state.Finish(id)
			found++
			continue
		}

		out = append(out, id)
	}

	if found > 0 {
		color.Cyan("Found %d file(s) already on disk", found)
		if serr := store.Save(); serr != nil {
			logctx.From(ctx).Warn("Save state", zap.Error(serr))
		}
	}

	return out
}

// existingNames indexes the file names that are already in the download
// directory, including one level of sub directories.
func existingNames(dir string) map[string]struct{} {
	out := make(map[string]struct{})

	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}

	for _, e := range entries {
		if e.IsDir() {
			sub, err := os.ReadDir(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}

			for _, s := range sub {
				if !s.IsDir() {
					out[s.Name()] = struct{}{}
				}
			}
			continue
		}

		out[e.Name()] = struct{}{}
	}

	return out
}

func (r *Runner) download(ctx context.Context, job *Job, link Link, dir string, ids []int,
	store *stateStore, state *State, threads, limit int) error {
	dialog, err := r.resolveDialog(ctx, job, link)
	if err != nil {
		return err
	}

	log := logctx.From(ctx)

	pw := prog.New(utils.Byte.FormatBinaryBytes)
	go pw.Render()

	jobCtx := &jobContext{
		job:    job,
		state:  state,
		store:  store,
		logger: log,
	}
	progress := newJobProgress(pw, jobCtx)

	opts := &iterOptions{
		template: r.template(),
		include:  r.opts.Include,
		exclude:  r.opts.Exclude,
		takeout:  r.opts.Takeout,
		batch:    DefaultBatchSize,
		progress: progress,
		onFinish: func(ids []int) {
			state.Finish(ids...)

			if serr := store.SaveThrottled(); serr != nil {
				log.Warn("Save state", zap.Error(serr))
			}
		},
		onSkip: func(ids []int) {
			state.Skip(ids...)
			progress.markSkipped(len(ids))

			if serr := store.SaveThrottled(); serr != nil {
				log.Warn("Save state", zap.Error(serr))
			}
		},
	}

	it, err := newIter(r.pool, r.manager, dialog, dir, ids, opts)
	if err != nil {
		return err
	}

	dl := tdl.New(tdl.Options{
		Pool:     r.pool,
		Threads:  threads,
		Iter:     it,
		Progress: progress,
	})
	// part level resume: a failed element keeps its temp file, the next run
	// only fetches the missing parts
	dl.SetSkipParts(true)

	log.Info("Start download",
		zap.Int("ids", len(ids)),
		zap.Int("threads", threads),
		zap.Int("limit", limit))

	err = dl.Download(ctx, limit)

	it.Drain()

	prog.Wait(ctx, pw)

	if serr := store.Save(); serr != nil {
		log.Warn("Save state", zap.Error(serr))
	}

	done, failed, skipped := progress.Stats()
	color.Cyan("Job summary: %d downloaded, %d failed, %d skipped", done, failed, skipped)

	// A whole range resolving to nothing is almost never real: it means the ids
	// were looked up in the wrong dialog, typically comment ids that were
	// resolved against the channel instead of its discussion group.
	if len(ids) > 0 && done == 0 && failed == 0 && skipped >= len(ids) {
		color.Red("None of the %d message(s) exist in dialog %d.", len(ids), dialog.ID())
		color.Red("Check --mode / the \"comment\" setting of the job and the id range, then run again with --retry-skipped.")
		log.Warn("Every target was unavailable",
			zap.Int("ids", len(ids)),
			zap.Int64("dialog", dialog.ID()),
			zap.Bool("comment_mode", commentDialog(job, link)))
	}

	if err != nil {
		return errors.Wrap(err, "download")
	}

	if itErr := it.Err(); itErr != nil {
		return errors.Wrap(itErr, "iterate")
	}

	return nil
}

// resolveDialog resolves the peer that actually holds the messages.
//
// Comment mode is what the python script expressed by appending ?comment=N to
// the post link: the message ids are comments, so they live in the linked
// discussion group of the channel and not in the channel itself. The mode is
// therefore taken from the job ("comment": true) as well as from the link
// (?comment=N is also accepted), because the python config points chat_url at
// the post and keeps the range in start_comment/end_comment.
func (r *Runner) resolveDialog(ctx context.Context, job *Job, link Link) (peers.Peer, error) {
	peer, err := tutil.GetInputPeer(ctx, r.manager, link.Chat)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve chat %q", link.Chat)
	}

	if !commentDialog(job, link) {
		return peer, nil
	}

	ch, ok := peer.(peers.Channel)
	if !ok {
		return nil, errors.Errorf("chat %q has no comment section", link.Chat)
	}

	raw, err := ch.FullRaw(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "get channel full")
	}

	linked, ok := raw.GetLinkedChatID()
	if !ok {
		return nil, errors.Errorf("chat %q has no linked discussion group", link.Chat)
	}

	group, err := r.manager.ResolveChannelID(ctx, linked)
	if err != nil {
		return nil, errors.Wrap(err, "resolve discussion group")
	}

	logctx.From(ctx).Debug("Resolve comment dialog",
		zap.String("chat", link.Chat),
		zap.Int64("linked", linked),
		zap.Int64("id", group.ID()))

	return group, nil
}

// commentDialog reports whether the message ids of a job are comment ids in the
// linked discussion group of the channel.
//
// Both spellings are accepted: the job level "comment": true of the python
// config and the ?comment=N syntax of a telegram link. They mean the same
// thing, and getting this wrong silently resolves the ids against the wrong
// chat, where every single one of them looks deleted.
func commentDialog(job *Job, link Link) bool {
	return link.CommentMode() || job.CommentMode()
}

// scanPeer resolves the dialog that incremental mode reads its window from.
//
// The python script exported job.chat when it was set and the chat of the link
// otherwise. An explicit job.chat is kept, because that is how the python
// config pointed the scan at a discussion group while chat_url kept pointing at
// the post.
func (r *Runner) scanPeer(ctx context.Context, job *Job, link Link) (peers.Peer, error) {
	ref := strings.TrimSpace(job.Chat)
	if ref == "" {
		return r.resolveDialog(ctx, job, link)
	}

	// a job.chat may also be a full telegram link
	if l, err := ParseLink(ref); err == nil {
		ref = l.Chat
	}

	return tutil.GetInputPeer(ctx, r.manager, ref)
}

func (r *Runner) template() string {
	if r.opts.Template != "" {
		return r.opts.Template
	}

	return `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`
}

// statePath returns the state file of a job. Every namespace and download
// directory gets its own file so two configs can't clobber each other.
func (r *Runner) statePath(job *Job) string {
	if r.opts.StateFile != "" {
		return r.opts.StateFile
	}

	if r.cfg.StateFile != "" {
		return r.cfg.StateFile
	}

	return filepath.Join(job.Dir(), DefaultStateFile)
}

// confirm asks the user a yes/no question.
func (r *Runner) confirm(ctx context.Context, question string, def bool) bool {
	if r.opts.Yes {
		color.Yellow("%s [auto: yes]", question)
		return true
	}

	return askConfirm(ctx, question, def)
}

// rangeIDs expands the start_comment/end_comment range, end excluded.
func rangeIDs(job *Job) []int {
	if job.StartComment == nil || job.EndComment == nil {
		return nil
	}

	start, end := *job.StartComment, *job.EndComment
	if end <= start {
		return nil
	}

	out := make([]int, 0, end-start)
	for id := start; id < end; id++ {
		out = append(out, id)
	}

	return out
}

// formatIDs renders an id list the way the python script displayed it.
func formatIDs(ids []int) string {
	if len(ids) == 0 {
		return "empty"
	}

	first, last := ids[0], ids[len(ids)-1]
	if last-first+1 == len(ids) {
		return fmt.Sprintf("%d-%d (%d)", first, last, len(ids))
	}

	return fmt.Sprintf("%d...%d (%d)", first, last, len(ids))
}

func pick(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}

	return values[len(values)-1]
}

func num(v *int) int {
	if v == nil {
		return 0
	}

	return *v
}

// askConfirm prints a y/n question and reads the answer from stdin.
func askConfirm(ctx context.Context, question string, def bool) bool {
	select {
	case <-ctx.Done():
		return def
	default:
	}

	hint := "y/N"
	if def {
		hint = "Y/n"
	}

	fmt.Printf("%s (%s): ", color.YellowString(question), hint)

	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		return def
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// Mode selects how the message ids of a job are interpreted.
const (
	// ModeAuto detects the mode from the url and config, like the python script.
	ModeAuto = "auto"
	// ModeComment treats every id as a comment id (?comment=N).
	ModeComment = "comment"
	// ModeDirect treats every id as a message id in the chat itself.
	ModeDirect = "direct"
)

// ResolveMode applies --mode to a job.
func (j *Job) ResolveMode(mode string) (string, error) {
	switch strings.ToLower(mode) {
	case "", ModeAuto:
		if j.CommentMode() {
			return ModeComment, nil
		}
		return ModeDirect, nil
	case ModeComment, ModeDirect:
		return strings.ToLower(mode), nil
	default:
		return "", errors.Errorf("invalid mode %q", mode)
	}
}
