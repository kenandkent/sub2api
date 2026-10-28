//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestWalletFundingSelfSpendLedgerReducesAvailable(t *testing.T) {
	ctx:=context.Background();tx:=testTx(t);var userID int64
	require.NoError(t,tx.QueryRowContext(ctx,`INSERT INTO users(email,password_hash,balance) VALUES($1,'hash',0) RETURNING id`,fmt.Sprintf("journal-%d@example.com",time.Now().UnixNano())).Scan(&userID))
	_,err:=tx.ExecContext(ctx,`SELECT marketplace_migrate_funding_user($1)`,userID);require.NoError(t,err)
	repo:=NewWalletFundingRepository()
	require.NoError(t,repo.Credit(ctx,tx,userID,fmt.Sprintf("credit-%d",userID),"test",decimal.NewFromInt(5),"cash_paid","test",fmt.Sprint(userID)))
	require.NoError(t,repo.SpendSelfOwned(ctx,tx,userID,fmt.Sprintf("spend-%d",userID),"test",decimal.NewFromInt(2)))
	var journal decimal.Decimal
	require.NoError(t,tx.QueryRowContext(ctx,`SELECT coalesce(sum(CASE side WHEN 'credit' THEN amount ELSE -amount END),0) FROM ledger_entries WHERE subject_id=$1 AND account='buyer_available'`,userID).Scan(&journal))
	require.Equal(t,"3",journal.String())
}

func TestWalletFundingLegacyMigrationAndBalanceWrites(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	var userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'hash',10) RETURNING id`, fmt.Sprintf("funding-legacy-%d@example.com", time.Now().UnixNano())).Scan(&userID))
	_, err := tx.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+2 WHERE id=$1`, userID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance-15 WHERE id=$1`, userID)
	require.NoError(t, err)
	var balance, debt, remaining decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT u.balance,s.debt,COALESCE((SELECT sum(remaining) FROM wallet_funding_lots WHERE user_id=u.id),0) FROM users u JOIN wallet_funding_states s ON s.user_id=u.id WHERE u.id=$1`, userID).Scan(&balance, &debt, &remaining))
	require.Equal(t, "-3", balance.String())
	require.Equal(t, "3", debt.String())
	require.True(t, remaining.IsZero())
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+5 WHERE id=$1`, userID)
	require.NoError(t, err)
	var promotional decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT sum(remaining) FROM wallet_funding_lots WHERE user_id=$1 AND kind='promotional'`, userID).Scan(&promotional))
	require.Equal(t, "2", promotional.String())
}

func TestWalletFundingLegacyUnmappedHoldFailsClosed(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	var userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance,frozen_balance) VALUES($1,'hash',7,3) RETURNING id`, fmt.Sprintf("funding-unmapped-%d@example.com", time.Now().UnixNano())).Scan(&userID))
	_, err := tx.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	require.ErrorContains(t, err, "unmapped")
}

func TestWalletFundingLegacyHoldAndOverdraftDoNotStealReservedMoney(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	var userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'hash',10) RETURNING id`, fmt.Sprintf("funding-hold-%d@example.com", time.Now().UnixNano())).Scan(&userID))
	_, err := tx.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT set_config('sub2api.legacy_hold_id',$1,true)`, fmt.Sprintf("batch_image_hold:test-%d", userID))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance-3,frozen_balance=frozen_balance+3 WHERE id=$1`, userID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance-9 WHERE id=$1`, userID)
	require.NoError(t, err)
	var reserved, debt decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT (SELECT sum(reserved) FROM wallet_funding_lots WHERE user_id=$1),debt FROM wallet_funding_states WHERE user_id=$1`, userID).Scan(&reserved, &debt))
	require.Equal(t, "3", reserved.String())
	require.Equal(t, "2", debt.String())
	// Cost 1 consumes the old hold; the released 2 pays the self-owned debt.
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+2,frozen_balance=frozen_balance-3 WHERE id=$1`, userID)
	require.NoError(t, err)
	var balance, frozen decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, userID).Scan(&balance, &frozen))
	require.True(t, balance.IsZero())
	require.True(t, frozen.IsZero())
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT debt FROM wallet_funding_states WHERE user_id=$1`, userID).Scan(&debt))
	require.True(t, debt.IsZero())
}

func TestWalletFundingLegacyCashNeedsMatchingPaidOrder(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	var userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'hash',0) RETURNING id`, fmt.Sprintf("funding-cash-%d@example.com", time.Now().UnixNano())).Scan(&userID))
	_, err := tx.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	require.NoError(t, err)
	code := fmt.Sprintf("funding-cash-%d", userID)
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_orders(user_id,amount,pay_amount,recharge_code,status,expires_at) VALUES($1,5,5,$2,'PAID',now()+interval '1 day')`, userID, code)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT set_config('sub2api.payment_redeem_code',$1,true)`, code)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+5 WHERE id=$1`, userID)
	require.NoError(t, err)
	var cash decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT sum(remaining) FROM wallet_funding_lots WHERE user_id=$1 AND kind='cash_paid'`, userID).Scan(&cash))
	require.Equal(t, "5", cash.String())
	// A non-matching increment cannot reuse the payment reference as cash.
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+1 WHERE id=$1`, userID)
	require.NoError(t, err)
	var promotional decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT sum(remaining) FROM wallet_funding_lots WHERE user_id=$1 AND kind='promotional'`, userID).Scan(&promotional))
	require.Equal(t, "1", promotional.String())
}

func TestWalletFundingLegacyMapsExistingBatchHoldExactlyOnce(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	var userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance,frozen_balance) VALUES($1,'hash',7,3) RETURNING id`, fmt.Sprintf("funding-existing-%d@example.com", time.Now().UnixNano())).Scan(&userID))
	batchID := fmt.Sprintf("funding-batch-%d", userID)
	holdKey := "batch_image_hold:" + batchID
	_, err := tx.ExecContext(ctx, `INSERT INTO batch_image_jobs(batch_id,user_id,api_key_id,provider,model,item_count,hold_amount) VALUES($1,$2,99,'gemini','test',1,3)`, batchID, userID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_billing_dedup(request_id,api_key_id,request_fingerprint) VALUES($1,99,'test')`, holdKey)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	require.NoError(t, err)
	var remaining, reserved decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT remaining,reserved FROM wallet_funding_lots WHERE user_id=$1`, userID).Scan(&remaining, &reserved))
	require.Equal(t, "10", remaining.String())
	require.Equal(t, "3", reserved.String())
	_, err = tx.ExecContext(ctx, `SELECT set_config('sub2api.legacy_hold_id',$1,true)`, holdKey)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+1,frozen_balance=frozen_balance-3 WHERE id=$1`, userID)
	require.NoError(t, err)
	var balance decimal.Decimal
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
	require.Equal(t, "8", balance.String())
}
