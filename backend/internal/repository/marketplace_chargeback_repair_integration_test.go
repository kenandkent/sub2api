//go:build integration

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChargebackBuyerLedgerRepairIsBalancedAndIdempotent(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	user, code := reviewUser(t, 100)
	reviewMigrate(t, user)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO marketplace_payment_chargebacks(event_id,order_id,user_id,actor_id,amount,revoked,buyer_debt,evidence) VALUES($1,1,$2,1,20,20,0,'historical')`, code, user)
	require.NoError(t, err)
	script, err := os.ReadFile("../../migrations/274_marketplace_chargeback_ledger_repair.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		tx, e := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, e)
		_, e = tx.ExecContext(ctx, string(script))
		require.NoError(t, e)
		require.NoError(t, tx.Commit())
	}
	var journals, entries int
	var net string
	require.NoError(t, integrationDB.QueryRow(`SELECT count(DISTINCT t.id),count(e.id),coalesce(sum(CASE e.side WHEN 'debit' THEN e.amount ELSE -e.amount END),0)::text FROM ledger_transactions t JOIN ledger_entries e ON e.transaction_id=t.id WHERE t.operation_id=$1 AND t.sealed`, "payment-chargeback-buyer:"+code).Scan(&journals, &entries, &net))
	require.Equal(t, 1, journals)
	require.Equal(t, 2, entries)
	require.Equal(t, "0.00000000", net)
}
