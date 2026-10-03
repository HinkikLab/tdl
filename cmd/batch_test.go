package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/iyear/tdl/pkg/autodl"
	"github.com/iyear/tdl/pkg/consts"
)

func TestBatchOptionsPreserveExplicitUnlimitedPool(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Int(consts.FlagPoolSize, 8, "")
	require.NoError(t, cmd.Flags().Set(consts.FlagPoolSize, "0"))

	opts := (&batchFlags{}).options(cmd)
	require.True(t, opts.PoolSizeSet)
	require.Zero(t, opts.PoolSize)
}

func TestBatchInitCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "examples with spaces", "config.json")
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
	require.Contains(t, out, "11 example jobs")
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
	_, err = run("init", "-c", path, "--force")
	require.NoError(t, err)
	_, err = autodl.LoadConfig(path)
	require.NoError(t, err)
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
