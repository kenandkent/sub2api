//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func waitFundingLockWaiters(t *testing.T, pattern string, count int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var got int
		err := integrationDB.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, pattern).Scan(&got)
		require.NoError(t, err)
		if got >= count {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("expected %d lock waiters matching %q", count, pattern)
}
func TestFundingAdminSelfRefundLockOrderDoesNotDeadlock(t *testing.T) {
	user, _, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	_, err := integrationDB.Exec(`UPDATE users SET role='admin' WHERE id=$1`, user)
	require.NoError(t, err)
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT id FROM payment_orders WHERE id=$1 FOR UPDATE`, order)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, e := port.ReclassifyLegacyFunding(ctx, service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: user, Amount: decimal.NewFromInt(1), Evidence: "verified current allocation", RemainingProvenanceConfirmed: true, OperationID: code + "-self"})
		done <- e
	}()
	waitFundingLockWaiters(t, "SELECT user_id,amount,bonus_amount,status,paid_at,completed_at,recharge_code%", 1)
	_, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='2s'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET status=status WHERE id=$1`, user)
	require.NoError(t, err, "refund holding order must acquire buyer row")
	require.NoError(t, tx.Commit())
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("reclassification did not finish")
	}
}
func TestFundingAdminCrossAdministratorLockOrderDoesNotDeadlock(t *testing.T) {
	a, b, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	_, err := integrationDB.Exec(`UPDATE users SET role='admin' WHERE id=$1`, a)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE users SET balance=25 WHERE id=$1`, b)
	require.NoError(t, err)
	reviewMigrate(t, b)
	orderA := reviewCreateOrder(t, a, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, a, orderA, code)
	codeB := strings.ReplaceAll(code, "-", "")[:30] + "bb"
	orderB := reviewCreateOrder(t, b, "COMPLETED", 0, codeB)
	reviewProofOfFulfillment(t, b, orderB, codeB)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE id IN ($1,$2) ORDER BY id FOR SHARE`, a, b)
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	done := make(chan error, 2)
	for _, v := range []struct {
		actor, user, order int64
		op                 string
	}{{a, b, orderB, code + "-ab"}, {b, a, orderA, code + "-ba"}} {
		go func(v struct {
			actor, user, order int64
			op                 string
		}) {
			_, e := port.ReclassifyLegacyFunding(ctx, service.FundingReclassCommand{UserID: v.user, OrderID: v.order, ActorID: v.actor, Amount: decimal.NewFromInt(1), Evidence: "verified current allocation", RemainingProvenanceConfirmed: true, OperationID: v.op})
			done <- e
		}(v)
	}
	deadline := time.Now().Add(8 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		err = integrationDB.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND (query LIKE 'SELECT balance,frozen_balance FROM users%' OR query LIKE 'SELECT id,role,status%')`).Scan(&got)
		require.NoError(t, err)
		if got >= 2 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	require.GreaterOrEqual(t, got, 2, fmt.Sprintf("expected two funding row waiters, got %d", got))
	require.NoError(t, tx.Commit())
	for i := 0; i < 2; i++ {
		select {
		case err = <-done:
			require.NoError(t, err)
		case <-time.After(8 * time.Second):
			t.Fatal("cross-admin reclassification did not finish")
		}
	}
}
