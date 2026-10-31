//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestClosedStatementSnapshotsDebtAndCaptureOffset(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	s, receipt, _, owner, _, _, captureLedger := marketplaceRefundFixture(t)
	// Snap to the first of the current month before stepping back: subtracting
	// a month from e.g. Oct 31 yields Sep 31, which normalizes into the current
	// month and would make GenerateClosedMonth reject an open statement.
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	ledger := NewMarketplaceLedgerRepository()
	require.NoError(t, ledger.Post(ctx, tx, s+":debt", "test", "USD", []LedgerPosting{
		{Account: "supplier_debt", SubjectID: &owner, Side: "debit", Amount: decimal.NewFromInt(5)},
		{Account: "test_clearing", Side: "credit", Amount: decimal.NewFromInt(5)},
	}))
	require.NoError(t, ledger.Post(ctx, tx, s+":offset", "test", "USD", []LedgerPosting{
		{Account: "test_clearing", Side: "debit", Amount: decimal.NewFromInt(2)},
		{Account: "supplier_debt", SubjectID: &owner, Side: "credit", Amount: decimal.NewFromInt(2)},
	}))
	require.NoError(t, tx.Commit())
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE ledger_transactions DISABLE TRIGGER ledger_transactions_immutable`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE ledger_transactions SET booked_at=CASE WHEN operation_id=$1 THEN $3::timestamptz-interval '1 day' ELSE $3::timestamptz+interval '1 day' END WHERE operation_id IN ($1,$2) OR id=$4`, s+":debt", s+":offset", month, captureLedger)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE ledger_transactions ENABLE TRIGGER ledger_transactions_immutable`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET supplier_debt_offset=2 WHERE receipt_id=$1`, receipt)
	require.NoError(t, err)
	repo := NewSupplierFinanceRepository(integrationDB)
	statement, err := repo.GenerateClosedMonth(ctx, owner, month)
	require.NoError(t, err)
	require.Equal(t, "5.00000000", statement.OpeningDebt)
	require.Equal(t, "3.00000000", statement.ClosingDebt)
	require.Equal(t, "2.00000000", statement.DebtOffset)
	entries, err := repo.StatementEntries(ctx, owner, statement.ID, 1, 100)
	require.NoError(t, err)
	require.Len(t, entries.Items, 1)
	require.Equal(t, "2.00000000", entries.Items[0].SupplierDebtOffset)
	// Recreate the pre-282 prototype schema, then run the real migration to
	// prove its backfill uses the statement's cutoff, not the live wallet.
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE supplier_statements DROP COLUMN opening_debt, DROP COLUMN closing_debt, DROP COLUMN debt_offset; ALTER TABLE supplier_statement_entries DROP COLUMN supplier_debt_offset`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("282_marketplace_statement_debt.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	again, err := repo.Statement(ctx, owner, statement.ID)
	require.NoError(t, err)
	require.Equal(t, statement.ClosingDebt, again.ClosingDebt)
	require.Equal(t, statement.OpeningDebt, again.OpeningDebt)
	require.Equal(t, statement.DebtOffset, again.DebtOffset)
	entries, err = repo.StatementEntries(ctx, owner, statement.ID, 1, 100)
	require.NoError(t, err)
	require.Equal(t, "2.00000000", entries.Items[0].SupplierDebtOffset)
}
