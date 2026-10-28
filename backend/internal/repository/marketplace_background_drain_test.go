package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDrainMaturityBatchesDrainsBeyondOneBatch(t *testing.T) {
	calls := 0
	n, err := drainMaturityBatches(context.Background(), 100, 10, func(context.Context, int) (int, error) {
		calls++
		if calls <= 2 {
			return 100, nil
		}
		return 3, nil
	})
	require.NoError(t, err)
	require.Equal(t, 203, n)
	require.Equal(t, 3, calls)
}

func TestDrainMaturityBatchesIsBounded(t *testing.T) {
	calls := 0
	n, err := drainMaturityBatches(context.Background(), 100, 3, func(context.Context, int) (int, error) {
		calls++
		return 100, nil
	})
	require.NoError(t, err)
	require.Equal(t, 300, n)
	require.Equal(t, 3, calls)
}
