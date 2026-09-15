package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

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
