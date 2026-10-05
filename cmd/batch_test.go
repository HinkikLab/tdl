package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/core/diagnostic"
	corei18n "github.com/iyear/tdl/core/i18n"
	"github.com/iyear/tdl/pkg/autodl"
	"github.com/iyear/tdl/pkg/console"
	"github.com/iyear/tdl/pkg/consts"
	pki18n "github.com/iyear/tdl/pkg/i18n"
)

func TestBatchOptionsPreserveExplicitUnlimitedPool(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Int(consts.FlagPoolSize, 8, "")
	require.NoError(t, cmd.Flags().Set(consts.FlagPoolSize, "0"))

	opts := (&batchFlags{}).options(cmd)
	require.True(t, opts.PoolSizeSet)
	require.Zero(t, opts.PoolSize)
}

func TestFlagParseErrorsAreLocalizedAndKeepTheirCause(t *testing.T) {
	translator, err := pki18n.New(corei18n.Chinese)
	require.NoError(t, err)
	root := NewWithTranslator(translator)
	root.SetArgs([]string{"--threads", "invalid"})
	err = root.Execute()
	require.Error(t, err)
	var diagnosticErr *diagnostic.Error
	require.ErrorAs(t, err, &diagnosticErr)
	require.Error(t, diagnosticErr.Cause)
	require.Contains(t, console.FormatError(err, translator), "参数 --threads 的值 \"invalid\" 无效（要求 int）")
	require.Contains(t, diagnosticErr.Error(), "Invalid value")
}

func TestArgumentValidationErrorsAreLocalizedAndKeepTheirCause(t *testing.T) {
	translator, err := pki18n.New(corei18n.Chinese)
	require.NoError(t, err)
	root := NewWithTranslator(translator)
	root.SetArgs([]string{"batch", "unexpected"})
	err = root.Execute()
	require.Error(t, err)
	var diagnosticErr *diagnostic.Error
	require.ErrorAs(t, err, &diagnosticErr)
	require.Error(t, diagnosticErr.Cause)
	require.Contains(t, console.FormatError(err, translator), "不接受位置参数")
	require.Contains(t, diagnosticErr.Error(), "unknown command")
}

func TestBatchInitCommand(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	path := filepath.Join(t.TempDir(), "examples with spaces", "config.yaml")
	run := func(args ...string) (string, error) {
		cmd := NewBatch()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run("init", "-c", path)
	require.NoError(t, err)
	require.Contains(t, out, "11 example jobs; English comments")
	require.Contains(t, out, "--check-only")
	cfg, err := autodl.LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Jobs, 11)

	require.NoError(t, os.WriteFile(path, []byte("keep"), 0o644))
	_, err = run("init", "-c", path)
	require.ErrorContains(t, err, "already exists")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "keep", string(b))
	out, err = run("init", "-c", path, "--force", "--lang", "zh")
	require.NoError(t, err)
	require.Contains(t, out, "Chinese comments")
	b, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(b), "# 01. 直接下载")
	_, err = autodl.LoadConfig(path)
	require.NoError(t, err)
	_, err = run("init", "-c", path, "--force", "--lang", "invalid")
	require.ErrorContains(t, err, "use auto, zh or en")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, b, after)
}

func TestBatchPoolOverridesGlobalUnlimitedPool(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Int(consts.FlagPoolSize, 8, "")
	require.NoError(t, cmd.Flags().Set(consts.FlagPoolSize, "0"))
	f := &batchFlags{pool: 4}
	cmd.Flags().Int("batch-pool", 0, "")
	require.NoError(t, cmd.Flags().Set("batch-pool", "4"))

	opts := f.options(cmd)
	require.True(t, opts.PoolSizeSet)
	require.Equal(t, 4, opts.PoolSize)
}

func TestBatchOfflineValidationDoesNotOpenAccountStorage(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"namespace":"config_account","download_base":"`+filepath.ToSlash(filepath.Join(root, "not-created"))+`","jobs":[{"chat_url":"https://t.me/course","incremental":true}]}`), 0o600))
	cmd := New()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"batch", "--validate-only", "--config", path, "--storage", "type=not-a-storage-driver"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "Batch configuration valid")
	require.Contains(t, output.String(), "namespace=config_account")
	require.NoDirExists(t, filepath.Join(root, "not-created"))
}

func TestBatchOverrideOriginIdentifiesBatchAndGlobalFlags(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Int(consts.FlagPoolSize, 8, "")
	cmd.Flags().Int(consts.FlagThreads, 4, "")
	cmd.Flags().Int(consts.FlagLimit, 2, "")
	cmd.Flags().Int("batch-pool", 0, "")
	require.NoError(t, cmd.Flags().Set(consts.FlagPoolSize, "0"))
	require.NoError(t, cmd.Flags().Set(consts.FlagThreads, "3"))
	opts := (&batchFlags{limit: 5}).options(cmd)
	require.Equal(t, "global flag", opts.Origins.Pool)
	require.Equal(t, "global flag", opts.Origins.Threads)
	require.Equal(t, "batch flag", opts.Origins.Limit)
}
