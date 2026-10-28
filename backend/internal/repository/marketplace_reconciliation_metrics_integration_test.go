//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReconciliationReportsBacklogAndBuyerLedgerDifference(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	_, receipt, buyer, _, _, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO marketplace_balance_invalidation_tasks(user_id,created_at) VALUES($1,now()-interval '1 hour')`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_cache_tasks(capture_id,user_id,api_key_id,created_at) SELECT c.id,r.buyer_user_id,r.api_key_id,now()-interval '10 minutes' FROM marketplace_captures c JOIN marketplace_requests r ON r.id=c.receipt_id WHERE c.receipt_id=$1`, receipt)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET matures_at=now()-interval '20 minutes' WHERE receipt_id=$1`, receipt)
	require.NoError(t, err)
	v, err := NewSupplierFinanceRepository(integrationDB).Reconciliation(ctx, "", 1, 100)
	require.NoError(t, err)
	require.EqualValues(t, 2, v.Totals.CacheBacklog)
	require.GreaterOrEqual(t, v.Totals.CacheOldestAgeSeconds, int64(3600))
	require.EqualValues(t, 1, v.Totals.MaturityBacklog)
	require.GreaterOrEqual(t, v.Totals.MaturityOldestAgeSeconds, int64(1200))
	require.EqualValues(t, 1, v.Totals.BuyerLedgerDifferences)
}
