//go:build integration

package repository

import (
	"context"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLedgerOriginalReferenceIsImmutableAndPartOfIdempotency(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	repo := NewMarketplaceLedgerRepository()
	op := uuid.NewString()
	entries := []LedgerPosting{{Account: "a", Side: "debit", Amount: decimal.NewFromInt(1)}, {Account: "b", Side: "credit", Amount: decimal.NewFromInt(1)}}
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repo.Post(ctx, tx, op, "test", "USD", entries))
	var id int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM ledger_transactions WHERE operation_id=$1`, op).Scan(&id))
	require.NoError(t, tx.Commit())
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repo.PostWithOriginal(ctx, tx, op+":refund", "test", "USD", id, entries))
	require.NoError(t, tx.Commit())
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repo.PostWithOriginal(ctx, tx, op+":refund", "test", "USD", id, entries))
	require.NoError(t, tx.Commit())
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.ErrorIs(t, repo.Post(ctx, tx, op+":refund", "test", "USD", entries), ErrFundingOperationConflict)
	require.NoError(t, tx.Rollback())
	_, err = integrationDB.ExecContext(ctx, `UPDATE ledger_transactions SET original_transaction_id=NULL WHERE operation_id=$1`, op+":refund")
	require.Error(t, err)
}
