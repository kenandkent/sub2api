package service

import (
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPayoutAmountUsesCentsAndMinimum(t *testing.T) {
	min := decimal.RequireFromString("10.00")
	require.NoError(t, ValidatePayoutAmount(decimal.RequireFromString("10.01"), min))
	require.Error(t, ValidatePayoutAmount(decimal.RequireFromString("10.001"), min))
	require.Error(t, ValidatePayoutAmount(decimal.RequireFromString("10.000"), min))
	require.Error(t, ValidatePayoutAmount(decimal.RequireFromString("9.99"), min))
	require.Error(t, ValidatePayoutAmount(decimal.Zero, min))
}

func TestPayoutReleasePaysDebtFirst(t *testing.T) {
	debt, available := PayoutRelease(decimal.RequireFromString("12.12345678"), decimal.RequireFromString("2.00000001"))
	require.True(t, debt.Equal(decimal.RequireFromString("2.00000001")))
	require.True(t, available.Equal(decimal.RequireFromString("10.12345677")))
}
