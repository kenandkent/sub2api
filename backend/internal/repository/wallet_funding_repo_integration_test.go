//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestWalletFundingConcurrentHoldsAndReplay(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("funding-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES ($1,'x',10) RETURNING id`, email).Scan(&user))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,10,10)`, user, email)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, e := integrationDB.BeginTx(ctx, nil)
			if e == nil {
				e = NewWalletFundingRepository().Hold(ctx, tx, user, fmt.Sprintf("hold-%s-%d", email, i), "test", decimal.NewFromInt(7))
				if e == nil {
					e = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
			}
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
	var b, h string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance::text,frozen_balance::text FROM users WHERE id=$1`, user).Scan(&b, &h))
	require.Equal(t, "3.00000000", b)
	require.Equal(t, "7.00000000", h)
}

func TestMarketplaceLedgerBalancedAndRollback(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	op := fmt.Sprintf("ledger-%d", time.Now().UnixNano())
	err = NewMarketplaceLedgerRepository().Post(ctx, tx, op, "test", "USD", []LedgerPosting{{Account: "buyer", Side: "debit", Amount: decimal.NewFromInt(2)}, {Account: "supplier", Side: "credit", Amount: decimal.NewFromInt(1)}})
	require.Error(t, err)
	require.NoError(t, tx.Rollback())
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM ledger_transactions WHERE operation_id=$1`, op).Scan(&n))
	require.Zero(t, n)
}

func TestWalletFundingOverdraftAndRelease(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("funding-debt-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES ($1,'x',10) RETURNING id`, email).Scan(&user))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,10,10)`, user, email)
	require.NoError(t, err)
	repo := NewWalletFundingRepository()
	execute := func(f func(*sql.Tx) error) {
		tx, e := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, e)
		e = f(tx)
		if e != nil {
			_ = tx.Rollback()
		}
		require.NoError(t, e)
		require.NoError(t, tx.Commit())
	}
	execute(func(tx *sql.Tx) error { return repo.Hold(ctx, tx, user, email+"-hold", "test", decimal.NewFromInt(7)) })
	execute(func(tx *sql.Tx) error { return repo.Hold(ctx, tx, user, email+"-hold", "test", decimal.NewFromInt(7)) })
	execute(func(tx *sql.Tx) error {
		return repo.SpendSelfOwned(ctx, tx, user, email+"-spend", "test", decimal.NewFromInt(5))
	})
	execute(func(tx *sql.Tx) error {
		return repo.SettleHold(ctx, tx, user, email+"-release", "test", email+"-hold", decimal.Zero)
	})
	var balance, hold, debt, remaining, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,u.frozen_balance,s.debt,l.remaining,l.reserved FROM users u JOIN wallet_funding_states s ON s.user_id=u.id JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1`, user).Scan(&balance, &hold, &debt, &remaining, &reserved))
	require.True(t, balance.Equal(decimal.NewFromInt(5)))
	require.True(t, hold.IsZero())
	require.True(t, debt.IsZero())
	require.True(t, remaining.Equal(decimal.NewFromInt(5)))
	require.True(t, reserved.IsZero())
}

func TestMarketplaceLedgerReplayAndImmutability(t *testing.T) {
	ctx := context.Background()
	op := fmt.Sprintf("ledger-replay-%d", time.Now().UnixNano())
	entries := []LedgerPosting{{Account: "a", Side: "debit", Amount: decimal.NewFromInt(2)}, {Account: "b", Side: "credit", Amount: decimal.NewFromInt(2)}}
	post := func(actor string, entries []LedgerPosting) error {
		tx, e := integrationDB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		e = NewMarketplaceLedgerRepository().Post(ctx, tx, op, actor, "USD", entries)
		if e != nil {
			_ = tx.Rollback()
			return e
		}
		return tx.Commit()
	}
	require.NoError(t, post("test", entries))
	require.NoError(t, post("test", entries))
	entries[1].Account = "different"
	require.Error(t, post("test", entries))
	_, err := integrationDB.ExecContext(ctx, `UPDATE ledger_entries SET amount=1 WHERE transaction_id=(SELECT id FROM ledger_transactions WHERE operation_id=$1)`, op)
	require.Error(t, err)
}

func TestMarketplaceLedgerDatabaseRejectsUnbalancedAndRollback(t *testing.T) {
	ctx := context.Background()
	op := fmt.Sprintf("ledger-rollback-%d", time.Now().UnixNano())
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	entries := []LedgerPosting{{Account: "a", Side: "debit", Amount: decimal.NewFromInt(1)}, {Account: "b", Side: "credit", Amount: decimal.NewFromInt(1)}}
	require.NoError(t, NewMarketplaceLedgerRepository().Post(ctx, tx, op, "test", "USD", entries))
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM ledger_transactions WHERE operation_id=$1`, op).Scan(&count))
	require.Zero(t, count)
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	var id int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint) VALUES ($1,'test','USD','x') RETURNING id`, op).Scan(&id))
	_, err = tx.ExecContext(ctx, `INSERT INTO ledger_entries(transaction_id,account,side,amount) VALUES ($1,'a','debit',1)`, id)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE ledger_transactions SET sealed=true WHERE id=$1`, id)
	require.NoError(t, err)
	require.Error(t, tx.Commit())
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM ledger_transactions WHERE operation_id=$1`, op).Scan(&count))
	require.Zero(t, count)
}

func TestWalletFundingCreditPaysDebtAndPromotionalIsExcluded(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("funding-credit-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES ($1,'x',-2) RETURNING id`, email).Scan(&user))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,debt) VALUES($1,2)`, user)
	require.NoError(t, err)
	repo := NewWalletFundingRepository()
	op := email + "-credit"
	credit := func() {
		tx, e := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, e)
		e = repo.Credit(ctx, tx, user, op, "test", decimal.NewFromInt(5), service.FundingPromotional, "test", email)
		require.NoError(t, e)
		require.NoError(t, tx.Commit())
	}
	credit()
	credit()
	var balance, debt, remaining decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,s.debt,l.remaining FROM users u JOIN wallet_funding_states s ON s.user_id=u.id JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1`, user).Scan(&balance, &debt, &remaining))
	require.True(t, balance.Equal(decimal.NewFromInt(3)))
	require.True(t, debt.IsZero())
	require.True(t, remaining.Equal(decimal.NewFromInt(3)))
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = repo.Hold(ctx, tx, user, email+"-hold", "test", decimal.NewFromInt(1))
	require.ErrorIs(t, err, service.ErrInsufficientFunding)
	require.NoError(t, tx.Rollback())
}

func TestWalletFundingPartialCaptureReleasesRemainder(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("funding-capture-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES ($1,'x',10) RETURNING id`, email).Scan(&user))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,10,10)`, user, email)
	require.NoError(t, err)
	repo := NewWalletFundingRepository()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repo.Hold(ctx, tx, user, email+"-hold", "test", decimal.NewFromInt(3)))
	require.NoError(t, tx.Commit())
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repo.SettleHold(ctx, tx, user, email+"-capture", "test", email+"-hold", decimal.NewFromInt(2)))
	require.NoError(t, tx.Commit())
	var balance, hold, remaining, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,u.frozen_balance,l.remaining,l.reserved FROM users u JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1`, user).Scan(&balance, &hold, &remaining, &reserved))
	require.True(t, balance.Equal(decimal.NewFromInt(8)))
	require.True(t, hold.IsZero())
	require.True(t, remaining.Equal(decimal.NewFromInt(8)))
	require.True(t, reserved.IsZero())
}
