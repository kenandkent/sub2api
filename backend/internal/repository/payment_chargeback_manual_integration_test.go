//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestManualChargebackExactOwnerReplayAndAudit(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',200000000) RETURNING id`, suffix+"@example.test").Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(suffix + "@example.test").SetUserName(suffix).SetAmount(200000000).SetPayAmount(200000000).SetFeeRate(0).SetRechargeCode(suffix).SetOutTradeNo(suffix).SetPaymentType("stripe").SetPaymentTradeNo(suffix).SetOrderType("balance").SetStatus("COMPLETED").SetPaidAt(time.Now()).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,200000000,200000000)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	amount := decimal.RequireFromString("123456789.12345678")
	for i := 0; i < 2; i++ {
		require.NoError(t, svc.RecordExternalChargebackExact(ctx, order.ID, 123, amount, suffix, "verified statement"))
	}
	require.Error(t, svc.RecordExternalChargebackExact(ctx, order.ID, 123, decimal.NewFromInt(1), suffix, "verified statement"))
	require.Error(t, svc.RecordExternalChargebackExact(ctx, order.ID, 124, amount, suffix, "verified statement"))
	require.Error(t, svc.RecordExternalChargebackExact(ctx, order.ID, 123, amount, suffix, "changed evidence"))
	var actual, balance decimal.Decimal
	var actualUser, count int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT user_id,amount FROM marketplace_payment_chargebacks WHERE event_id=$1`, suffix).Scan(&actualUser, &actual))
	require.Equal(t, user, actualUser)
	require.True(t, amount.Equal(actual))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user).Scan(&balance))
	require.True(t, decimal.RequireFromString("76543210.87654322").Equal(balance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_payment_chargebacks WHERE event_id=$1`, suffix).Scan(&count))
	require.EqualValues(t, 1, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM payment_audit_logs WHERE order_id=$1 AND action LIKE 'PAYMENT_CHARGEBACK:%'`, order.ID).Scan(&count))
	require.GreaterOrEqual(t, count, int64(1))
}
