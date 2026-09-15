package tutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBestThreadsAlwaysReturnsPositiveCount(t *testing.T) {
	require.Equal(t, 1, BestThreads(1024, 0))
	require.Equal(t, 1, BestThreads(1024, -1))
	require.Equal(t, 1, BestThreads(1024, 8))
	require.Equal(t, 8, BestThreads(100<<20, 8))
}
