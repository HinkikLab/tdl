package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/iyear/tdl/app/extension"
	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/core/storage"
	extbase "github.com/iyear/tdl/extension"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/extensions"
	pki18n "github.com/iyear/tdl/pkg/i18n"
	"github.com/iyear/tdl/pkg/tclient"
)

func NewExtension(em *extensions.Manager) *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:     "extension",
		Short:   "Manage tdl extensions",
		GroupID: groupTools.ID,
		Aliases: []string{"extensions", "ext"},
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			em.SetDryRun(dryRun)
		},
	}

	cmd.AddCommand(NewExtensionList(em), NewExtensionInstall(em), NewExtensionRemove(em), NewExtensionUpgrade(em))

	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "only print what would be done without actually doing it")

	return cmd
}

func NewExtensionList(em *extensions.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List installed extension commands",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return extension.List(cmd.Context(), em)
		},
	}

	return cmd
}

func NewExtensionInstall(em *extensions.Manager) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install a tdl extension",
		Args:  pki18n.MinimumArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return extension.Install(cmd.Context(), em, args, force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "force install even if extension already exists")

	return cmd
}

func NewExtensionUpgrade(em *extensions.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade a tdl extension",
		RunE: func(cmd *cobra.Command, args []string) error {
			return extension.Upgrade(cmd.Context(), em, args)
		},
	}

	return cmd
}

func NewExtensionRemove(em *extensions.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove an installed extension",
		Args:  pki18n.MinimumArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return extension.Remove(cmd.Context(), em, args)
		},
	}

	return cmd
}

func NewExtensionCmd(em *extensions.Manager, ext extensions.Extension, stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   ext.Name(),
		Short: fmt.Sprintf("Extension %s", ext.Name()),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			opts, err := tOptions(ctx)
			if err != nil {
				return diagnostic.Describe(errors.Wrap(err, "build telegram options"), corei18n.Message{ID: "errors.context.build_telegram_options", Args: map[string]any{"Reason": err}})
			}
			app, err := tclient.GetApp(opts.KV)
			if err != nil {
				return diagnostic.Describe(errors.Wrap(err, "get app"), corei18n.Message{ID: "errors.context.get_app", Args: map[string]any{"Reason": err}})
			}

			session, err := storage.NewSession(opts.KV, false).LoadSession(ctx)
			if err != nil {
				return diagnostic.Describe(errors.Wrap(err, "load session"), corei18n.Message{ID: "errors.context.load_session", Args: map[string]any{"Reason": err}})
			}

			dataDir := filepath.Join(consts.ExtensionsDataPath, ext.Name())
			if err = os.MkdirAll(dataDir, 0o755); err != nil {
				return diagnostic.Describe(errors.Wrap(err, "create extension data dir"), corei18n.Message{ID: "errors.context.create_extension_data_dir", Args: map[string]any{"Reason": err}})
			}

			language := string(corei18n.English)
			if translator := corei18n.FromContext(ctx); translator != nil {
				language = string(translator.Language())
			}
			env := &extbase.Env{
				Name:      ext.Name(),
				AppID:     app.AppID,
				AppHash:   app.AppHash,
				Session:   session,
				Namespace: viper.GetString(consts.FlagNamespace),
				DataDir:   dataDir,
				NTP:       opts.NTP,
				Proxy:     opts.Proxy,
				Pool:      viper.GetInt64(consts.FlagPoolSize),
				Debug:     viper.GetBool(consts.FlagDebug),
				Language:  language,
			}

			if err = em.Dispatch(ext, args, env, stdin, stdout, stderr); err != nil {
				var execError *exec.ExitError
				if errors.As(err, &execError) {
					return execError
				}
				return diagnostic.Describe(fmt.Errorf("failed to run extension: %w", err), corei18n.Message{ID: "errors.message.failed_to_run_extension_value", Args: map[string]any{"Arg1": err}})
			}
			return nil
		},
		GroupID:            groupExtensions.ID,
		DisableFlagParsing: true,
	}
}
