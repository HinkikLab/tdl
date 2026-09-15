package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/pkg/autodl"
	"github.com/iyear/tdl/pkg/consts"
)

// batchDisableEnv disables the automatic batch start of a bare `tdl`
// invocation, which is handy for scripts that want the help output.
const batchDisableEnv = "TDL_NO_BATCH"

// batchFlags holds the batch command line options. They mirror the python
// script arguments one to one, plus the tdl specific performance overrides.
type batchFlags struct {
	config    string
	checkOnly bool
	yes       bool
	mode      string

	incremental    bool
	stateFile      string
	overlapSeconds int
	retrySkipped   bool

	dir      string
	template string
	include  []string
	exclude  []string
	takeout  bool

	threads int
	limit   int
	pool    int
}

// options turns the parsed flags into autodl options.
//
// --batch-* wins over the global -l/-t/--pool, which in turn wins over the
// performance keys of config.json.
func (f *batchFlags) options(global *cobra.Command) autodl.Options {
	globalPool, globalPoolSet := changedIntValue(global, consts.FlagPoolSize)
	poolSize, poolSizeSet := globalPool, globalPoolSet
	if changed(global, "batch-pool") {
		poolSize, poolSizeSet = f.pool, true
	}
	opts := autodl.Options{
		ConfigPath:   f.config,
		Dir:          f.dir,
		Template:     f.template,
		Include:      f.include,
		Exclude:      f.exclude,
		Takeout:      f.takeout,
		Threads:      pickInt(f.threads, changedInt(global, consts.FlagThreads)),
		Limit:        pickInt(f.limit, changedInt(global, consts.FlagLimit)),
		PoolSize:     poolSize,
		PoolSizeSet:  poolSizeSet,
		CheckOnly:    f.checkOnly,
		Yes:          f.yes,
		Mode:         f.mode,
		Incremental:  f.incremental,
		StateFile:    f.stateFile,
		RetrySkipped: f.retrySkipped,
		Delay:        viper.GetDuration(consts.FlagDelay),
	}

	if f.overlapSeconds >= 0 {
		v := f.overlapSeconds
		opts.OverlapSeconds = &v
	}

	return opts
}

// changedInt returns the persistent flag value when the user set it explicitly.
func changedInt(cmd *cobra.Command, name string) int {
	v, _ := changedIntValue(cmd, name)
	return v
}

func changedIntValue(cmd *cobra.Command, name string) (int, bool) {
	f := cmd.Flags().Lookup(name)
	if f == nil || !f.Changed {
		return 0, false
	}

	v, err := cmd.Flags().GetInt(name)
	if err != nil {
		return 0, false
	}

	return v, true
}

// changed reports whether the user set a persistent flag explicitly.
func changed(cmd *cobra.Command, name string) bool {
	f := cmd.Flags().Lookup(name)

	return f != nil && f.Changed
}

func pickInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}

	return 0
}

// NewBatch builds the `tdl batch` command.
//
// It is a native port of python/run_unified.py: it reads the same config.json
// and supports the same modes and resume semantics, but it drives tdl's own
// downloader instead of spawning a tdl process per batch.
func NewBatch() *cobra.Command {
	f := &batchFlags{}

	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Batch download from a run_unified.py compatible config.json",
		Long: `Batch download a list of telegram messages or comments described by a
config.json, using the same format as python/run_unified.py.

Without --config, config.json in the working directory is used. Running bare
` + "`tdl`" + ` (without any argument) starts this command automatically when such a
config exists and the account is logged in.

Every job is resumable: finished messages are recorded in a state file next to
the downloads, and partially downloaded files keep their parts so only the
missing ones are fetched again.`,
		GroupID: groupTools.ID,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for name, value := range map[string]int{
				"batch-threads": f.threads,
				"batch-limit":   f.limit,
				"batch-pool":    f.pool,
			} {
				if value < 0 {
					return fmt.Errorf("--%s must not be negative", name)
				}
			}
			for _, name := range []string{consts.FlagThreads, consts.FlagLimit, consts.FlagPoolSize} {
				if value, set := changedIntValue(cmd, name); set && value < 0 {
					return fmt.Errorf("--%s must not be negative", name)
				}
			}
			if f.overlapSeconds < -1 {
				return errors.New("--overlap-seconds must not be less than -1")
			}

			cfg := f.config
			if cfg == "" {
				if found, ok := autodl.FindConfig(); ok {
					cfg = found
				} else {
					cfg = autodl.DefaultConfigFile
				}
			}

			parsed, err := autodl.LoadConfigForRun(cfg, f.incremental)
			if err != nil {
				return err
			}

			// the namespace of the config is honoured unless the user picked
			// one explicitly with -n/--ns
			if ns := strings.TrimSpace(parsed.Namespace); ns != "" && !changed(cmd, consts.FlagNamespace) {
				viper.Set(consts.FlagNamespace, ns)

				if cmd.Flags().Lookup(consts.FlagNamespace) != nil {
					_ = cmd.Flags().Set(consts.FlagNamespace, ns)
				}
			}

			opts := f.options(cmd)
			opts.ConfigPath = cfg

			return tRun(cmd.Context(), func(ctx context.Context, c *telegram.Client, kvd storage.Storage) error {
				return autodl.Run(logctx.Named(ctx, "batch"), c, kvd, opts)
			})
		},
	}

	cmd.Flags().StringVarP(&f.config, "config", "c", "", "config file path (default: config.json in the working directory)")
	cmd.Flags().BoolVar(&f.checkOnly, "check-only", false, "only check and report what would be downloaded")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "answer yes to every confirmation")
	cmd.Flags().StringVar(&f.mode, "mode", autodl.ModeAuto, fmt.Sprintf("download mode: [%s] (overrides the \"comment\" setting of a job)",
		strings.Join([]string{autodl.ModeAuto, autodl.ModeComment, autodl.ModeDirect}, ", ")))

	cmd.Flags().BoolVar(&f.incremental, "incremental", false, "force incremental mode for every job")
	cmd.Flags().StringVar(&f.stateFile, "state-file", "", "state file path (default: <download dir>/tdl_state.json)")
	cmd.Flags().IntVar(&f.overlapSeconds, "overlap-seconds", -1, "incremental lookback window in seconds")
	cmd.Flags().BoolVar(&f.retrySkipped, "retry-skipped", false, "retry messages that were previously recorded as unavailable")

	cmd.Flags().StringVarP(&f.dir, "dir", "d", "", "override the download directory of every job")
	cmd.Flags().StringVar(&f.template, "template", "", "download file name template")
	cmd.Flags().StringSliceVarP(&f.include, "include", "i", []string{}, "include the specified file extensions")
	cmd.Flags().StringSliceVarP(&f.exclude, "exclude", "e", []string{}, "exclude the specified file extensions")
	cmd.Flags().BoolVar(&f.takeout, "takeout", false, "use takeout sessions, which have lower flood wait limits")

	cmd.Flags().IntVar(&f.threads, "batch-threads", 0, "max threads for one file (overrides the config and -t)")
	cmd.Flags().IntVar(&f.limit, "batch-limit", 0, "max concurrent files (overrides the config and -l)")
	cmd.Flags().IntVar(&f.pool, "batch-pool", 0, "connection pool size (overrides the config and --pool)")

	_ = cmd.MarkFlagDirname("dir")
	cmd.MarkFlagsMutuallyExclusive("include", "exclude")

	return cmd
}
