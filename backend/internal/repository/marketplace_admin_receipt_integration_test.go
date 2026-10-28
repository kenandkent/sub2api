//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestAdminReceiptShowsActualRemainingRefundAndRequiresActiveAdmin(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	s, receipt, buyer, _, admin, _, _ := marketplaceRefundFixture(t)
	repo := NewSupplierFinanceRepository(integrationDB)
	v, err := repo.AdminReceipt(ctx, admin, receipt)
	require.NoError(t, err)
	require.True(t, v.Captured)
	require.Equal(t, "3.00000000", v.ActualCharge)
	require.Equal(t, "3.00000000", v.RefundableAmount)
	require.NoError(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(1), s+":receipt"))
	v, err = repo.AdminReceipt(ctx, admin, receipt)
	require.NoError(t, err)
	require.Equal(t, "1.00000000", v.RefundedAmount)
	require.Equal(t, "2.00000000", v.RefundableAmount)
	_, err = repo.AdminReceipt(ctx, buyer, receipt)
	require.Error(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET status='inactive' WHERE id=$1`, admin)
	require.NoError(t, err)
	_, err = repo.AdminReceipt(ctx, admin, receipt)
	require.Error(t, err)
}
