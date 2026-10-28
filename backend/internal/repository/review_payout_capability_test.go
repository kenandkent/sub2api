package repository

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestSuspendedSupplierPayoutRemainsIndependentlyControlled(t *testing.T) {
	wallet := payoutWallet{status: "suspended", debt: decimal.Zero}
	require.True(t, canPayout(wallet))
	wallet.frozen = true
	require.False(t, canPayout(wallet))
	wallet.frozen = false
	wallet.debt = decimal.NewFromInt(1)
	require.False(t, canPayout(wallet))
}
