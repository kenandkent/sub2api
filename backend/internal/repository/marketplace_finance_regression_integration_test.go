//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func reviewCreateOrder(t *testing.T, user int64, status string, refunded float64, code string) int64 {
	t.Helper()
	o, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(code + "@example.test").SetUserName(code).SetAmount(100).SetBonusAmount(20).SetPayAmount(80).SetRefundAmount(refunded).SetFeeRate(0).SetRechargeCode(code).SetOutTradeNo(code).SetPaymentType("stripe").SetPaymentTradeNo(code).SetOrderType("balance").SetStatus(status).SetPaidAt(time.Now()).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(context.Background())
	require.NoError(t, err)
	return o.ID
}
func reviewUser(t *testing.T, balance int) (int64, string) {
	t.Helper()
	code := uuid.NewString()
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',$2) RETURNING id`, code+"@example.test", balance).Scan(&user))
	return user, code
}
func reviewMigrate(t *testing.T, user int64) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `SELECT marketplace_migrate_funding_user($1)`, user)
	require.NoError(t, err)
}
func reviewCharge(t *testing.T, order int64, amount int, event string) error {
	t.Helper()
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	return svc.RecordExternalChargebackExact(context.Background(), order, 123, decimal.NewFromInt(int64(amount)), event, "verified evidence")
}

func TestReviewHistoricalRefundChargebackCap(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	for _, tc := range []struct {
		name, status    string
		refunded        float64
		balance, amount int
	}{{"partial", "PARTIALLY_REFUNDED", 80, 20, 21}, {"full", "REFUNDED", 100, 0, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			user, code := reviewUser(t, tc.balance)
			order := reviewCreateOrder(t, user, tc.status, tc.refunded, code)
			reviewMigrate(t, user)
			err := reviewCharge(t, order, tc.amount, code+"-event")
			var debt decimal.Decimal
			require.NoError(t, integrationDB.QueryRow(`SELECT debt FROM wallet_funding_states WHERE user_id=$1`, user).Scan(&debt))
			t.Logf("status=%s credited_refunded=%.0f chargeback=%d actual_error=%v buyer_debt=%s", tc.status, tc.refunded, tc.amount, err, debt)
			if err == nil {
				t.Errorf("expected chargeback over remaining credited cap %.0f to be rejected", 100-tc.refunded)
			}
		})
	}
}

func TestReviewChargebackBuyerLedgerMatchesProjection(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		legacy bool
	}{{"legacy_debt", true}, {"post_migration_cash", false}} {
		t.Run(tc.name, func(t *testing.T) {
			initial := 0
			if tc.legacy {
				initial = 100
			}
			user, code := reviewUser(t, initial)
			reviewMigrate(t, user)
			order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
			if !tc.legacy {
				tx, err := integrationDB.BeginTx(ctx, nil)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, `SELECT set_config('sub2api.payment_redeem_code',$1,true)`, code)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+100 WHERE id=$1`, user)
				require.NoError(t, err)
				require.NoError(t, tx.Commit())
			}
			ledger := func() decimal.Decimal {
				var n decimal.Decimal
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT coalesce(sum(CASE e.side WHEN 'credit' THEN e.amount ELSE -e.amount END),0) FROM ledger_entries e JOIN ledger_transactions lt ON lt.id=e.transaction_id WHERE lt.sealed AND e.subject_id=$1 AND e.account='buyer_available'`, user).Scan(&n))
				return n
			}
			require.True(t, ledger().Equal(decimal.NewFromInt(100)), "opening ledger must equal balance")
			require.NoError(t, reviewCharge(t, order, 20, code+"-event"))
			var balance, debt decimal.Decimal
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,s.debt FROM users u JOIN wallet_funding_states s ON s.user_id=u.id WHERE u.id=$1`, user).Scan(&balance, &debt))
			l := ledger()
			rec, err := NewSupplierFinanceRepository(integrationDB).Reconciliation(ctx, "", 1, 100)
			require.NoError(t, err)
			t.Logf("balance=%s debt=%s buyer_available_ledger=%s funding_differences=%d wallet_differences=%d", balance, debt, l, rec.Totals.FundingDifferences, rec.Totals.WalletDifferences)
			if !l.Equal(balance) {
				t.Errorf("buyer available ledger %s != users.balance %s", l, balance)
			}
		})
	}
	_ = fmt.Sprint
}

func TestHistoricalRefundBridgeDoesNotDoubleCountModernRefund(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	user, code := reviewUser(t, 0)
	reviewMigrate(t, user)
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	sqltx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = sqltx.ExecContext(ctx, `SELECT set_config('sub2api.payment_redeem_code',$1,true)`, code)
	require.NoError(t, err)
	_, err = sqltx.ExecContext(ctx, `UPDATE users SET balance=balance+100 WHERE id=$1`, user)
	require.NoError(t, err)
	require.NoError(t, sqltx.Commit())
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	_, err = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order, user, 40, code+"-refund")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	tx, err = integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.SettlePaymentRefund(dbent.NewTxContext(ctx, tx), code+"-refund", true))
	require.NoError(t, tx.Commit())
	_, err = integrationDB.ExecContext(ctx, `UPDATE payment_orders SET status='PARTIALLY_REFUNDED',refund_amount=40 WHERE id=$1`, order)
	require.NoError(t, err)
	require.NoError(t, reviewCharge(t, order, 60, code+"-chargeback"), "modern refund attempt and order mirror represent one $40 refund")
}
