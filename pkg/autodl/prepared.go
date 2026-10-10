package autodl

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/go-faster/errors"
	"gopkg.in/yaml.v3"

	"github.com/iyear/tdl/app/chat"
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/util/fsutil"
	"github.com/iyear/tdl/internal/transfer"
	"github.com/iyear/tdl/pkg/console"
)

// EffectiveOptions contains the values chosen once, before opening an account.
type EffectiveOptions struct {
	Namespace            string
	Pool, Threads, Limit int
}

// OptionOrigins describes why each effective value was chosen.
type OptionOrigins struct{ Namespace, Pool, Threads, Limit string }

// Origin labels used by OptionOrigins. Callers may supply a more specific
// override label, such as the flag that set the value.
const (
	OriginDefault   = "default"
	OriginConfig    = "config"
	OriginOverride  = "override"
	originNamespace = "namespace override"
)

// resolveOrigin labels a value chosen from an override, the config or a default.
func resolveOrigin(override bool, label string, config bool) string {
	switch {
	case override && label != "":
		return label
	case override:
		return OriginOverride
	case config:
		return OriginConfig
	default:
		return OriginDefault
	}
}

// PreparedRun is an immutable configuration snapshot. Its configuration and
// overrides are private so callers cannot change account/update requirements
// after preparing the run.
type PreparedRun struct {
	cfg       *Config
	opts      Options
	effective EffectiveOptions
	origins   OptionOrigins
}

func (p *PreparedRun) EffectiveOptions() EffectiveOptions { return p.effective }
func (p *PreparedRun) Origins() OptionOrigins             { return p.origins }
func (p *PreparedRun) ConfigPath() string                 { return p.opts.ConfigPath }
func (p *PreparedRun) NeedsBotUpdates() bool {
	for _, job := range p.cfg.Jobs {
		if job.FollowLinks {
			return true
		}
	}
	return false
}
func (p *PreparedRun) BotUpdates() *chat.BotUpdates { return p.opts.BotUpdates }

// Prepare validates a single file snapshot without contacting Telegram or
// creating output/state files.
func Prepare(opts Options) (*PreparedRun, error) {
	cfg, err := LoadConfigForRun(opts.ConfigPath, opts.Incremental)
	if err != nil {
		return nil, err
	}
	return prepareConfig(cfg, opts)
}

func prepareConfig(cfg *Config, opts Options) (*PreparedRun, error) {
	// Clone the compatibility DTO; execution never mutates the caller's jobs.
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var copyConfig Config
	if err := yaml.Unmarshal(encoded, &copyConfig); err != nil {
		return nil, err
	}
	if err := copyConfig.Normalize(); err != nil {
		return nil, err
	}
	cfg = &copyConfig
	opts.Include = append([]string(nil), opts.Include...)
	opts.Exclude = append([]string(nil), opts.Exclude...)
	opts.Middlewares = append(opts.Middlewares[:0:0], opts.Middlewares...)
	if opts.OverlapSeconds != nil {
		v := *opts.OverlapSeconds
		opts.OverlapSeconds = &v
	}
	if opts.BotUpdates == nil {
		for _, job := range cfg.Jobs {
			if job.FollowLinks {
				opts.BotUpdates = &chat.BotUpdates{}
				break
			}
		}
	}
	if opts.Mode == "" {
		opts.Mode = ModeAuto
	}
	opts.Mode = strings.ToLower(opts.Mode)
	if opts.Mode != ModeAuto && opts.Mode != ModeComment && opts.Mode != ModeDirect {
		return nil, &ConfigError{Path: opts.ConfigPath, Err: diagnostic.Describe(errors.Errorf("invalid mode %q", opts.Mode), corei18n.Message{ID: "errors.message.invalid_mode_value", Args: map[string]any{"Arg1": fmt.Sprintf("%q", opts.Mode)}})}
	}
	if opts.PoolSize < 0 || opts.Threads < 0 || opts.Limit < 0 || opts.Delay < 0 {
		return nil, &ConfigError{Path: opts.ConfigPath, Err: diagnostic.Describe(errors.New("performance overrides and delay must not be negative"), corei18n.Message{ID: "errors.message.performance_overrides_and_delay_must_not_be_negative"})}
	}
	if opts.OverlapSeconds != nil && *opts.OverlapSeconds < 0 {
		return nil, &ConfigError{Path: opts.ConfigPath, Err: diagnostic.Describe(errors.New("overlap_seconds override must not be negative"), corei18n.Message{ID: "errors.message.overlap_key_seconds_override_must_not_be_negative"})}
	}
	if len(opts.Include) > 0 && len(opts.Exclude) > 0 {
		return nil, &ConfigError{Path: opts.ConfigPath, Err: diagnostic.Describe(errors.New("include and exclude cannot be combined"), corei18n.Message{ID: "errors.message.include_and_exclude_cannot_be_combined"})}
	}
	ns := strings.TrimSpace(opts.Namespace)
	configNamespace := !opts.NamespaceSet && strings.TrimSpace(cfg.Namespace) != ""
	if configNamespace {
		ns = strings.TrimSpace(cfg.Namespace)
	}
	if ns == "" {
		ns = "default"
	}
	opts.Namespace = ns
	poolOverride := opts.PoolSizeSet || opts.PoolSize > 0
	pool := DefaultPoolSize
	if poolOverride {
		pool = opts.PoolSize
	} else if cfg.Pool != nil {
		pool = *cfg.Pool
	}
	threads, limit := pick(opts.Threads, num(cfg.Threads), DefaultThreads), pick(opts.Limit, num(cfg.Limit), DefaultLimit)
	origins := OptionOrigins{
		Namespace: resolveOrigin(opts.NamespaceSet, originNamespace, configNamespace),
		Pool:      resolveOrigin(poolOverride, opts.Origins.Pool, cfg.Pool != nil),
		Threads:   resolveOrigin(opts.Threads > 0, opts.Origins.Threads, cfg.Threads != nil),
		Limit:     resolveOrigin(opts.Limit > 0, opts.Origins.Limit, cfg.Limit != nil),
	}
	opts.PoolSize, opts.PoolSizeSet, opts.Threads, opts.Limit = pool, true, threads, limit
	r := &Runner{cfg: cfg, opts: opts}
	tpl, err := newNameTemplate(r.template())
	if err != nil {
		return nil, &ConfigError{Path: opts.ConfigPath, Err: err}
	}
	paths := make(map[string]int)
	staticOutputs := make(map[string]int)
	var reservations transfer.Reservations
	for i := range cfg.Jobs {
		job := &cfg.Jobs[i]
		wrap := func(err error) error {
			return &ConfigError{Path: opts.ConfigPath, Job: i + 1, ChatURL: job.ChatURL, Err: err}
		}
		if !job.IsTagJob() && !job.FollowLinks {
			mode, err := job.ResolveMode(opts.Mode)
			if err != nil {
				return nil, wrap(err)
			}
			job.mode = mode
			if strings.TrimSpace(job.ExportFilter) != "" {
				if _, err := expr.Compile(job.ExportFilter, expr.AsBool()); err != nil {
					return nil, wrap(diagnostic.Describe(errors.Wrap(err, "export_filter"), corei18n.Message{ID: "errors.context.export_key_filter", Args: map[string]any{"Reason": err}}))
				}
			}
		}
		dir := job.Dir()
		if opts.Dir != "" {
			dir = filepath.Join(opts.Dir, job.Subdir)
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, wrap(err)
		}
		job.dir = filepath.Clean(abs)
		// Every state file has one job owner; this is checked before login.
		if !job.IsTagJob() && !job.FollowLinks || job.UsesIncremental(cfg.Incremental) {
			key := canonicalPath(r.statePath(job))
			if earlier, exists := paths[key]; exists {
				return nil, wrap(diagnostic.Describe(errors.Errorf("state file %s is also owned by job %d; give each job a distinct subdir/state path", key, earlier), corei18n.Message{ID: "errors.batch.state_path_conflict", Args: map[string]any{"Arg1": key, "Arg2": earlier}}))
			}
			paths[key] = i + 1
			if err := reservations.ReserveState(key, fmt.Sprintf("job %d state", i+1)); err != nil {
				return nil, wrap(err)
			}
		}
		if !job.IsTagJob() && !job.FollowLinks {
			a, err := tpl.execute(&fileTemplate{DialogID: 1, MessageID: 1, FileName: "first.mp4"})
			if err != nil {
				return nil, wrap(diagnostic.Describe(errors.Wrap(err, "template"), corei18n.Message{ID: "errors.context.template", Args: map[string]any{"Reason": err}}))
			}
			b, err := tpl.execute(&fileTemplate{DialogID: 2, MessageID: 2, FileName: "second.mp4"})
			if err != nil {
				return nil, wrap(diagnostic.Describe(errors.Wrap(err, "template"), corei18n.Message{ID: "errors.context.template", Args: map[string]any{"Reason": err}}))
			}
			if _, err := fsutil.JoinWithin(job.dir, a); err != nil {
				return nil, wrap(err)
			}
			if a == b {
				path := canonicalPath(filepath.Join(job.dir, a))
				if earlier, exists := staticOutputs[path]; exists {
					return nil, wrap(diagnostic.Describe(errors.Errorf("template output %s conflicts with job %d", path, earlier), corei18n.Message{ID: "errors.message.template_output_value_conflicts_with_job_value", Args: map[string]any{"Arg1": path, "Arg2": earlier}}))
				}
				staticOutputs[path] = i + 1
				if _, err := reservations.Reserve(path, fmt.Sprintf("job %d template", i+1)); err != nil {
					return nil, wrap(err)
				}
				if num(job.EndComment)-num(job.StartComment) > 1 {
					return nil, wrap(diagnostic.Describe(errors.New("template produces the same output path for multiple messages"), corei18n.Message{ID: "errors.message.template_produces_the_same_output_path_for_multiple_messages"}))
				}
			}
		}
	}
	// The final paths belong to the snapshot, including after cwd changes.
	if opts.StateFile != "" {
		opts.StateFile = canonicalPath(opts.StateFile)
	}
	if cfg.StateFile != "" {
		cfg.StateFile = canonicalPath(cfg.StateFile)
	}
	opts.Dir = ""
	return &PreparedRun{cfg: cfg, opts: opts, effective: EffectiveOptions{Namespace: ns, Pool: pool, Threads: threads, Limit: limit}, origins: origins}, nil
}

func canonicalPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (p *PreparedRun) String() string {
	return fmt.Sprintf("%d jobs; namespace=%s pool=%d threads=%d limit=%d", len(p.cfg.Jobs), p.effective.Namespace, p.effective.Pool, p.effective.Threads, p.effective.Limit)
}

func (p *PreparedRun) Summary(ctx context.Context) string {
	return console.Translate(ctx, corei18n.Message{ID: "batch.summary", Default: "{{.Jobs}} jobs; namespace={{.Namespace}} pool={{.Pool}} threads={{.Threads}} limit={{.Limit}}", Args: map[string]any{"Jobs": len(p.cfg.Jobs), "Namespace": p.effective.Namespace, "Pool": p.effective.Pool, "Threads": p.effective.Threads, "Limit": p.effective.Limit}})
}
