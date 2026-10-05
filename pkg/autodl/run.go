package autodl

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
	"go.uber.org/multierr"
	"go.uber.org/zap"

	"github.com/iyear/tdl/app/chat"
	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/internal/transfer"
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
	// PoolSizeSet distinguishes an explicit zero (unlimited connections) from
	// an unset override.
	PoolSizeSet bool
	Threads     int
	Limit       int
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

	// SkipResumePrompt is retained for source compatibility. Batch resumes
	// automatically and has no resume prompt; use Yes for retry confirmations.
	SkipResumePrompt bool

	// Middlewares are extra telegram middlewares.
	Middlewares []telegram.Middleware
	BotUpdates  *chat.BotUpdates
	// Namespace and NamespaceSet preserve CLI > config > default account precedence.
	Namespace    string
	NamespaceSet bool
	// OnJobResult receives an explicit terminal result for each processed job.
	OnJobResult func(JobResult)
	// Origins labels explicit overrides for the prepared run's provenance.
	Origins OptionOrigins
}

// Runner executes a batch config against one authorized telegram client.
type Runner struct {
	opts     Options
	cfg      *Config
	client   *telegram.Client
	storage  storage.Storage
	poolSize int
	account  string

	pool         dcpool.Pool
	manager      *peers.Manager
	dialogs      map[string]peers.Peer
	unavailable  *chat.UnavailableLinks
	reservations transfer.Reservations
	report       *jobReport
}

// Run executes the batch download described by path.
//
// It is intentionally a drop-in replacement for python/run_unified.py: the
// same config.json, the same job semantics, but driven by tdl's own downloader
// so one process, one connection pool and batched requests are used for the
// whole run.
func Run(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts Options) error {
	prepared, err := Prepare(opts)
	if err != nil {
		return err
	}

	return RunPrepared(ctx, c, kvd, prepared)
}

// RunPrepared executes the exact snapshot validated before account setup.
func RunPrepared(ctx context.Context, c *telegram.Client, kvd storage.Storage, prepared *PreparedRun) error {
	if prepared == nil {
		return errors.New("nil prepared batch run")
	}
	return runPrepared(ctx, c, kvd, prepared.cfg, prepared.opts)
}

func run(ctx context.Context, c *telegram.Client, kvd storage.Storage, cfg *Config, opts Options) (rerr error) {
	prepared, err := prepareConfig(cfg, opts)
	if err != nil {
		return err
	}
	return RunPrepared(ctx, c, kvd, prepared)
}

func runPrepared(ctx context.Context, c *telegram.Client, kvd storage.Storage, cfg *Config, opts Options) (rerr error) {
	poolSize, threads, limit := opts.PoolSize, opts.Threads, opts.Limit

	r := &Runner{opts: opts, cfg: cfg, client: c, storage: kvd, poolSize: poolSize, unavailable: &chat.UnavailableLinks{}}
	if err := r.reserveStates(); err != nil {
		return err
	}
	middlewares := append(tclient.NewDefaultMiddlewares(ctx, 5*time.Minute), opts.Middlewares...)
	r.pool = dcpool.NewPool(c, int64(poolSize), middlewares...)
	defer multierr.AppendInvoke(&rerr, multierr.Close(r.pool))

	r.manager = peers.Options{Storage: storage.NewPeers(kvd)}.Build(r.pool.Default(ctx))
	self, err := c.Self(ctx)
	if err != nil {
		return errors.Wrap(err, "resolve authorized batch account")
	}
	r.account = fmt.Sprintf("%s:user:%d", opts.Namespace, self.ID)

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
	var failures error
	for idx := range cfg.Jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		job := &cfg.Jobs[idx]
		r.beginJob(idx+1, job)

		jobCtx := logctx.With(ctx, log.Named(fmt.Sprintf("job%d", idx+1)))

		announced := false
		announce := func(target string) {
			color.Blue("\n[%d/%d] %s", idx+1, len(cfg.Jobs), target)
			color.Cyan("Source: %s", job.ChatURL)
			announced = true
		}
		err := r.runJob(jobCtx, job, threads, limit, announce)
		result := r.finishJob(err)
		if !announced {
			announce(job.ChatURL)
		}
		color.Cyan("%s", formatJobSummary(result))
		if err != nil {
			failed++
			multierr.AppendInto(&failures, errors.Wrapf(err, "job %d (%s)", idx+1, job.ChatURL))
			log.Error("Job failed", zap.Int("job", idx+1), zap.Error(err))
			color.Red("Job %d failed: %s", idx+1, err)
		}
	}

	color.Green("\nBatch download finished: %d job(s), %d failed", len(cfg.Jobs), failed)
	if failed > 0 {
		return errors.Wrapf(failures, "%d job(s) failed", failed)
	}

	return nil
}

func (r *Runner) reserveStates() error {
	for i := range r.cfg.Jobs {
		job := &r.cfg.Jobs[i]
		if job.IsTagJob() || job.FollowLinks {
			if !job.UsesIncremental(r.cfg.Incremental) {
				continue
			}
		}
		if err := r.reservations.ReserveState(r.statePath(job), fmt.Sprintf("job %d state", i+1)); err != nil {
			return errors.Wrapf(err, "job %d state ownership", i+1)
		}
	}
	return nil
}

// missing returns the targets that still have to be downloaded.
//
// Media completions must be resolved again to confirm current media identity
// and final-file size/stat. Only explicitly skipped or filtered messages bypass
// the lookup; a stale finished ID alone cannot prove that its file exists.
func (r *Runner) missing(targets []int, state *State) []int {
	missing := make([]int, 0, len(targets))
	for _, id := range targets {
		if !state.IsTerminalWithoutMedia(id) {
			missing = append(missing, id)
		}
	}
	return missing
}
