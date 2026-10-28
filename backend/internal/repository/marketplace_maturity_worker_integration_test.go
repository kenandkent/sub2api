//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMaturityPassCoordinatesInstances(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	_, receipt, _, _, _, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET matures_at=now()-interval '1 minute' WHERE receipt_id=$1`, receipt)
	require.NoError(t, err)
	lock, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = lock.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(75084321640951481))
	require.NoError(t, err)
	w := &MarketplaceBackgroundWorker{db: integrationDB, settlement: service.NewSupplierSettlementService(NewSupplierSettlementRepository(integrationDB))}
	require.NoError(t, w.maturePass(ctx))
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_captures WHERE receipt_id=$1`, receipt).Scan(&status))
	require.Equal(t, "pending", status)
	require.NoError(t, lock.Rollback())
	// A single-connection pool must still make progress; the coordination
	// lock must not consume the only connection needed by settlement.
	integrationDB.SetMaxOpenConns(1)
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(0) })
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	require.NoError(t, w.maturePass(bounded))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_captures WHERE receipt_id=$1`, receipt).Scan(&status))
	require.Equal(t, "matured", status)
}

func TestMaturityFailureDoesNotStarveLaterSuppliers(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	_, badReceipt, _, badOwner, _, _, _ := marketplaceRefundFixture(t)
	_, goodReceipt, _, goodOwner, _, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET matures_at=CASE WHEN receipt_id=$1 THEN now()-interval '2 hours' ELSE now()-interval '1 hour' END WHERE receipt_id IN ($1,$2)`, badReceipt, goodReceipt)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_wallets SET pending=0 WHERE user_id=$1`, badOwner)
	require.NoError(t, err)
	repo := NewSupplierSettlementRepository(integrationDB)
	// With a single-row batch, an unprocessable earliest capture must be
	// deferred so that the next pass reaches a later supplier.
	count, err := repo.MatureDue(ctx, time.Now(), 1)
	require.ErrorContains(t, err, "maturity pending invariant")
	require.Zero(t, count)
	count, err = repo.MatureDue(ctx, time.Now(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var badStatus, goodStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_captures WHERE receipt_id=$1`, badReceipt).Scan(&badStatus))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_captures WHERE receipt_id=$1`, goodReceipt).Scan(&goodStatus))
	require.Equal(t, "pending", badStatus)
	require.Equal(t, "matured", goodStatus)
	var failures int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT attempts FROM marketplace_maturity_failures f JOIN marketplace_captures c ON c.id=f.capture_id WHERE c.receipt_id=$1`, badReceipt).Scan(&failures))
	require.Equal(t, 1, failures)
	recon, err := NewSupplierFinanceRepository(integrationDB).Reconciliation(ctx, "", 1, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, recon.Totals.MaturityFailures)
	var available string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT available::text FROM supplier_wallets WHERE user_id=$1`, goodOwner).Scan(&available))
	require.Equal(t, "2.00000000", available)
	// Once the underlying discrepancy is resolved, the next eligible retry
	// matures the record and removes its failure marker.
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_wallets SET pending=2 WHERE user_id=$1`, badOwner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_maturity_failures SET retry_after=now()-interval '1 second'`)
	require.NoError(t, err)
	count, err = repo.MatureDue(ctx, time.Now(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	recon, err = NewSupplierFinanceRepository(integrationDB).Reconciliation(ctx, "", 1, 100)
	require.NoError(t, err)
	require.Zero(t, recon.Totals.MaturityFailures)
}

func TestMaturityLockedWalletDoesNotStarveLaterSuppliers(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	_, badReceipt, _, badOwner, _, _, _ := marketplaceRefundFixture(t)
	_, goodReceipt, _, _, _, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET matures_at=CASE WHEN receipt_id=$1 THEN now()-interval '2 hours' ELSE now()-interval '1 hour' END WHERE receipt_id IN ($1,$2)`, badReceipt, goodReceipt)
	require.NoError(t, err)
	lock, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer lock.Rollback()
	var ignored int64
	require.NoError(t, lock.QueryRowContext(ctx, `SELECT user_id FROM supplier_wallets WHERE user_id=$1 FOR UPDATE`, badOwner).Scan(&ignored))
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	repo := NewSupplierSettlementRepository(integrationDB)
	count, err := repo.MatureDue(deadline, time.Now(), 1)
	require.ErrorContains(t, err, "lock timeout")
	require.Zero(t, count)
	count, err = repo.MatureDue(deadline, time.Now(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
