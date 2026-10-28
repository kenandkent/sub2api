//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func createRefundTestOrder(t *testing.T, user int64, email string, amount float64) int64 {
	t.Helper()
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(amount).SetPayAmount(amount).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo(email).SetOrderType("balance").SetStatus("COMPLETED").SetPaidAt(time.Now()).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(context.Background())
	require.NoError(t, err)
	return order.ID
}

func TestPaymentFundingRefundRequiresOwnAvailableCashLot(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-source-%d@example.test", time.Now().UnixNano())
	var user, lot int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',15) RETURNING id`, email).Scan(&user))
	order := createRefundTestOrder(t, user, email, 5)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5) RETURNING id`, user, fmt.Sprint(order)).Scan(&lot))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'promotional','test',$2,10,10)`, user, email)
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	run := func(amount float64, attempt string) error {
		tx, e := integrationEntClient.Tx(ctx)
		require.NoError(t, e)
		txctx := dbent.NewTxContext(ctx, tx)
		_, e = repo.ReservePaymentRefund(txctx, order, user, amount, attempt)
		if e != nil {
			require.NoError(t, tx.Rollback())
			return e
		}
		return tx.Commit()
	}
	require.Error(t, run(6, email+"-too-much"), "promotional funds cannot make up an order's shortfall")
	require.NoError(t, run(4, email+"-reserve"))
	// An exact replay does not consume the cap twice.
	require.NoError(t, run(4, email+"-reserve"))
	require.ErrorIs(t, run(2, email+"-over-cap"), service.ErrInsufficientFunding)
	var balance, frozen, remaining, reserved string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance::text,u.frozen_balance::text,l.remaining::text,l.reserved::text FROM users u JOIN wallet_funding_lots l ON l.id=$2 WHERE u.id=$1`, user, lot).Scan(&balance, &frozen, &remaining, &reserved))
	require.Equal(t, "11.00000000", balance)
	require.Equal(t, "4.00000000", frozen)
	require.Equal(t, "5.00000000", remaining)
	require.Equal(t, "4.00000000", reserved)
	var allocated decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT reserved FROM wallet_funding_allocations WHERE hold_operation_id=$1 AND lot_id=$2`, "payment-refund:"+email+"-reserve", lot).Scan(&allocated))
	require.True(t, allocated.Equal(decimal.NewFromInt(4)))
}

func TestPaymentFundingRefundReleasePaysInterveningDebtAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-debt-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',10) RETURNING id`, email).Scan(&user))
	order := createRefundTestOrder(t, user, email, 10)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,10,10)`, user, fmt.Sprint(order))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	attempt := email + "-hold"
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	migrated, err := repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order, user, 4, attempt)
	require.True(t, migrated)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	sqltx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().SpendSelfOwned(ctx, sqltx, user, email+"-spend", "test", decimal.NewFromInt(7)))
	require.NoError(t, sqltx.Commit())
	for i := 0; i < 2; i++ {
		tx, err = integrationEntClient.Tx(ctx)
		require.NoError(t, err)
		require.NoError(t, repo.SettlePaymentRefund(dbent.NewTxContext(ctx, tx), attempt, false))
		require.NoError(t, tx.Commit())
	}
	var balance, frozen, debt, remaining, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,u.frozen_balance,s.debt,l.remaining,l.reserved FROM users u JOIN wallet_funding_states s ON s.user_id=u.id JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1`, user).Scan(&balance, &frozen, &debt, &remaining, &reserved))
	require.True(t, balance.Equal(decimal.NewFromInt(3)))
	require.True(t, frozen.IsZero())
	require.True(t, debt.IsZero())
	require.True(t, remaining.Equal(decimal.NewFromInt(3)))
	require.True(t, reserved.IsZero())
}

func TestPaymentChargebackRevokesOwnFundsAndRecordsDebt(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("chargeback-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',3) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo(email).SetOrderType("balance").SetStatus("COMPLETED").SetPaidAt(time.Now()).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,3)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	for i := 0; i < 2; i++ {
		tx, e := integrationEntClient.Tx(ctx)
		require.NoError(t, e)
		e = repo.RecordPaymentChargeback(dbent.NewTxContext(ctx, tx), order.ID, user, 123, 5, email, "bank evidence")
		require.NoError(t, e)
		require.NoError(t, tx.Commit())
	}
	var balance, debt, remaining decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,s.debt,l.remaining FROM users u JOIN wallet_funding_states s ON s.user_id=u.id JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1`, user).Scan(&balance, &debt, &remaining))
	require.True(t, balance.Equal(decimal.NewFromInt(-2)))
	require.True(t, debt.Equal(decimal.NewFromInt(2)))
	require.True(t, remaining.IsZero())
	// A later credit retires the debt, but the chargeback still blocks new shared holds.
	sqltx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().Credit(ctx, sqltx, user, email+"-new-credit", "test", decimal.NewFromInt(10), service.FundingCashPaid, "test", email))
	require.NoError(t, sqltx.Commit())
	sqltx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = NewWalletFundingRepository().Hold(ctx, sqltx, user, email+"-blocked-hold", "test", decimal.NewFromInt(1))
	require.ErrorIs(t, err, service.ErrInsufficientFunding)
	require.NoError(t, sqltx.Rollback())
}

func TestPaymentFundingRefundExecuteUsesSourceEvenWhenDeductDisabled(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-execute-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	result, err := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, GatewayAmount: 3, Reason: "test", Force: true, DeductBalance: false})
	require.NoError(t, err)
	require.True(t, result.Success)
	var balance, frozen, remaining, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,u.frozen_balance,l.remaining,l.reserved FROM users u JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1`, user).Scan(&balance, &frozen, &remaining, &reserved))
	require.True(t, balance.Equal(decimal.NewFromInt(2)))
	require.True(t, frozen.IsZero())
	require.True(t, remaining.Equal(decimal.NewFromInt(2)))
	require.True(t, reserved.IsZero())
}

func TestPaymentFundingRefundRejectsUnreconciledMigrationVersion(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-version-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,0)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order','987654',5,5)`, user)
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), 987654, user, 1, email)
	require.Error(t, err)
}

func TestPaymentFundingRefundSerializesWithConsumption(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-race-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order := createRefundTestOrder(t, user, email, 5)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		tx, e := integrationEntClient.Tx(ctx)
		if e == nil {
			_, e = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order, user, 4, email)
			if e == nil {
				e = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		done <- e
	}()
	go func() {
		<-start
		tx, e := integrationDB.BeginTx(ctx, nil)
		if e == nil {
			e = NewWalletFundingRepository().SpendSelfOwned(ctx, tx, user, email+"-spend", "test", decimal.NewFromInt(3))
			if e == nil {
				e = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		done <- e
	}()
	close(start)
	first, second := <-done, <-done
	require.True(t, first == nil || second == nil)
	var balance, frozen, debt, total, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,u.frozen_balance,s.debt,sum(l.remaining),sum(l.reserved) FROM users u JOIN wallet_funding_states s ON s.user_id=u.id JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1 GROUP BY u.id,s.debt`, user).Scan(&balance, &frozen, &debt, &total, &reserved))
	require.True(t, total.Sub(debt).Equal(balance.Add(frozen)))
	require.True(t, reserved.Equal(frozen))
	require.True(t, reserved.Equal(decimal.Zero) || reserved.Equal(decimal.NewFromInt(4)))
}

type refundGatewayFake struct {
	submitStatus, queryStatus string
	submitErr                 error
	queryID                   string
	calls                     int
	entered                   chan struct{}
	resume                    chan struct{}
}

func (g *refundGatewayFake) Name() string                          { return "fake" }
func (g *refundGatewayFake) ProviderKey() string                   { return "fake" }
func (g *refundGatewayFake) SupportedTypes() []payment.PaymentType { return nil }
func (g *refundGatewayFake) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unused")
}
func (g *refundGatewayFake) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	panic("unused")
}
func (g *refundGatewayFake) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	panic("unused")
}
func (g *refundGatewayFake) Refund(_ context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	g.calls++
	if g.entered != nil {
		close(g.entered)
		<-g.resume
	}
	if g.submitErr != nil {
		return nil, g.submitErr
	}
	return &payment.RefundResponse{Status: g.submitStatus, RefundID: "fake-refund-" + req.OrderID}, nil
}
func (g *refundGatewayFake) QueryRefundAttempt(ctx context.Context, req payment.RefundQueryRequest, attempt string) (*payment.RefundResponse, error) {
	if attempt == "" {
		return nil, errors.New("missing attempt")
	}
	return g.QueryRefund(ctx, req)
}
func (g *refundGatewayFake) QueryRefund(_ context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	id := "fake-refund-" + req.OrderID
	if g.queryID != "" {
		id = g.queryID
	}
	return &payment.RefundResponse{Status: g.queryStatus, RefundID: id}, nil
}

func TestPaymentFundingRefundUnknownRetainsHoldAcrossQuery(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-gateway-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &refundGatewayFake{submitErr: errors.New("connection lost"), queryStatus: "UNKNOWN"}
	newService := func() *service.PaymentService {
		s := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
		s.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
		return s
	}
	svc := newService()
	result, err := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, gateway.calls)
	// A fresh service represents a process restart; an unknown query must retain the hold.
	svc = newService()
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, gateway.calls)
	var frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id=$1`, user).Scan(&frozen))
	require.True(t, frozen.Equal(decimal.NewFromInt(3)))
	gateway.queryStatus = payment.ProviderStatusSuccess
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id=$1`, user).Scan(&frozen))
	require.True(t, frozen.IsZero())
	require.Equal(t, 1, gateway.calls)
}

func TestPaymentFundingRefundCertainFailureRestoresSameLot(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-failure-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',8) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5),($1,'promotional','gift',$3,3,3)`, user, fmt.Sprint(order.ID), email)
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &refundGatewayFake{submitStatus: payment.ProviderStatusFailed}
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	svc.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
	result, err := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
	require.NoError(t, err)
	require.False(t, result.Success)
	var balance, frozen, cash, gift decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,u.frozen_balance,(SELECT remaining FROM wallet_funding_lots WHERE user_id=u.id AND kind='cash_paid'),(SELECT remaining FROM wallet_funding_lots WHERE user_id=u.id AND kind='promotional') FROM users u WHERE id=$1`, user).Scan(&balance, &frozen, &cash, &gift))
	require.True(t, balance.Equal(decimal.NewFromInt(8)))
	require.True(t, frozen.IsZero())
	require.True(t, cash.Equal(decimal.NewFromInt(5)))
	require.True(t, gift.Equal(decimal.NewFromInt(3)))
	gateway.submitStatus = payment.ProviderStatusSuccess
	result, err = svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "retry"})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 2, gateway.calls)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, user).Scan(&balance, &frozen))
	require.True(t, balance.Equal(decimal.NewFromInt(5)))
	require.True(t, frozen.IsZero())
}

func TestPaymentFundingRefundPendingFailureReleasesAfterRestart(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-gateway-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &refundGatewayFake{submitStatus: payment.ProviderStatusPending, queryStatus: payment.ProviderStatusFailed}
	newService := func() *service.PaymentService {
		s := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
		s.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
		return s
	}
	svc := newService()
	result, err := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, gateway.calls)
	// A fresh service finalizes the same durable attempt after a failure query.
	svc = newService()
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	var balance, frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, user).Scan(&balance, &frozen))
	require.True(t, balance.Equal(decimal.NewFromInt(5)))
	require.True(t, frozen.IsZero())
	require.Equal(t, 1, gateway.calls)
}

func TestPaymentFundingRefundRecoversClaimAfterRestart(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-crash-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	txctx := dbent.NewTxContext(ctx, tx)
	_, err = tx.PaymentOrder.UpdateOneID(order.ID).SetStatus(service.OrderStatusRefunding).SetRefundAmount(3).SetRefundReason("test").Save(txctx)
	require.NoError(t, err)
	migrated, err := repo.ReservePaymentRefund(txctx, order.ID, user, 3, email)
	require.True(t, migrated)
	require.NoError(t, err)
	_, err = tx.PaymentAuditLog.Create().SetOrderID(fmt.Sprint(order.ID)).SetAction("REFUND_FUNDING_RESERVED").SetDetail(fmt.Sprintf(`{"attemptID":%q,"amount":3}`, email)).SetOperator("admin").Save(txctx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	gateway := &refundGatewayFake{queryStatus: "UNKNOWN"}
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	svc.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	var needsReview bool
	var auditCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT needs_review FROM marketplace_payment_refund_attempts WHERE attempt_id=$1`, email).Scan(&needsReview))
	require.True(t, needsReview)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM payment_audit_logs WHERE order_id=$1 AND action LIKE 'REFUND_NEEDS_REVIEW:%'`, fmt.Sprint(order.ID)).Scan(&auditCount))
	require.Equal(t, 1, auditCount)
	gateway.queryStatus = payment.ProviderStatusFailed
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success, "a query failure while the submitter may be live is not safe to release")
	gateway.queryStatus = payment.ProviderStatusSuccess
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Zero(t, gateway.calls)
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
}

func TestPaymentFundingRefundLatePendingCannotUndoRecoveredSuccess(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-race-gateway-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &refundGatewayFake{submitStatus: payment.ProviderStatusPending, queryStatus: payment.ProviderStatusSuccess, entered: make(chan struct{}), resume: make(chan struct{})}
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	svc.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
	done := make(chan error, 1)
	go func() {
		_, e := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
		done <- e
	}()
	<-gateway.entered
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	close(gateway.resume)
	require.NoError(t, <-done)
	var status string
	var balance, frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM payment_orders WHERE id=$1`, order.ID).Scan(&status))
	require.Equal(t, service.OrderStatusPartiallyRefunded, status)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, user).Scan(&balance, &frozen))
	require.True(t, balance.Equal(decimal.NewFromInt(2)))
	require.True(t, frozen.IsZero())
}

func TestPaymentFundingRefundLegacyDeductsOnceBeforeProvider(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-legacy-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	result, err := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "legacy", DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 3})
	require.NoError(t, err)
	require.True(t, result.Success)
	var balance decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user).Scan(&balance))
	require.True(t, balance.Equal(decimal.NewFromInt(2)))
}

type unprovenRefundGateway struct{ g *refundGatewayFake }

func (u *unprovenRefundGateway) Name() string                          { return u.g.Name() }
func (u *unprovenRefundGateway) ProviderKey() string                   { return u.g.ProviderKey() }
func (u *unprovenRefundGateway) SupportedTypes() []payment.PaymentType { return nil }
func (u *unprovenRefundGateway) CreatePayment(ctx context.Context, r payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return u.g.CreatePayment(ctx, r)
}
func (u *unprovenRefundGateway) QueryOrder(ctx context.Context, n string) (*payment.QueryOrderResponse, error) {
	return u.g.QueryOrder(ctx, n)
}
func (u *unprovenRefundGateway) VerifyNotification(ctx context.Context, raw string, h map[string]string) (*payment.PaymentNotification, error) {
	return u.g.VerifyNotification(ctx, raw, h)
}
func (u *unprovenRefundGateway) Refund(ctx context.Context, r payment.RefundRequest) (*payment.RefundResponse, error) {
	return u.g.Refund(ctx, r)
}
func (u *unprovenRefundGateway) QueryRefund(ctx context.Context, r payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	return u.g.QueryRefund(ctx, r)
}

func TestPaymentFundingRefundUnprovenSuccessRemainsHeld(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-unproven-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &unprovenRefundGateway{g: &refundGatewayFake{submitErr: errors.New("connection lost"), queryStatus: payment.ProviderStatusSuccess}}
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	svc.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
	result, err := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
	require.NoError(t, err)
	require.False(t, result.Success)
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "manual review")
	var frozen decimal.Decimal
	var needs bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.frozen_balance,a.needs_review FROM users u JOIN marketplace_payment_refund_attempts a ON a.user_id=u.id WHERE u.id=$1`, user).Scan(&frozen, &needs))
	require.True(t, frozen.Equal(decimal.NewFromInt(3)))
	require.True(t, needs)
}

func TestPaymentFundingRefundManualReconciliation(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-manual-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &unprovenRefundGateway{g: &refundGatewayFake{submitErr: errors.New("timeout"), queryStatus: payment.ProviderStatusSuccess}}
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	svc.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
	_, err = svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
	require.NoError(t, err)
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	queue, err := svc.ListPendingFundingRefunds(ctx, 100)
	require.NoError(t, err)
	var attempt string
	for _, item := range queue {
		if item.OrderID == order.ID {
			attempt = item.AttemptID
		}
	}
	require.NotEmpty(t, attempt)
	cmd := service.UncertainFundingResolution{OrderID: order.ID, AttemptID: attempt, ActorID: 123, Decision: "confirmed_refunded", ProviderRefundID: email + "-provider", Evidence: "bank statement verified", OperationID: email + "-op"}
	invalid := cmd
	invalid.OrderID++
	_, err = svc.ResolveUncertainFunding(ctx, invalid)
	require.Error(t, err)
	invalid = cmd
	invalid.Evidence = " "
	_, err = svc.ResolveUncertainFunding(ctx, invalid)
	require.Error(t, err)
	invalid = cmd
	invalid.ProviderRefundID = ""
	_, err = svc.ResolveUncertainFunding(ctx, invalid)
	require.Error(t, err)
	_, err = integrationEntClient.PaymentOrder.UpdateOneID(order.ID).SetRefundAmount(4).Save(ctx)
	require.NoError(t, err)
	_, err = svc.ResolveUncertainFunding(ctx, cmd)
	require.Error(t, err)
	_, err = integrationEntClient.PaymentOrder.UpdateOneID(order.ID).SetRefundAmount(3).Save(ctx)
	require.NoError(t, err)
	// A provider reference already attached to another attempt cannot be reused.
	otherEmail := email + "-other"
	var otherUser int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',1) RETURNING id`, otherEmail).Scan(&otherUser))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, otherUser)
	require.NoError(t, err)
	otherOrder := createRefundTestOrder(t, otherUser, otherEmail, 1)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,1,1)`, otherUser, fmt.Sprint(otherOrder))
	require.NoError(t, err)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	txctx := dbent.NewTxContext(ctx, tx)
	_, err = repo.ReservePaymentRefund(txctx, otherOrder, otherUser, 1, otherEmail)
	require.NoError(t, err)
	require.NoError(t, repo.BindRefundProviderReference(txctx, otherEmail, cmd.ProviderRefundID))
	require.NoError(t, tx.Commit())
	_, err = svc.ResolveUncertainFunding(ctx, cmd)
	require.Error(t, err)
	cmd.ProviderRefundID += "-unique"
	result, err := svc.ResolveUncertainFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Success)
	result, err = svc.ResolveUncertainFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Success)
	cmd.Evidence = "changed"
	_, err = svc.ResolveUncertainFunding(ctx, cmd)
	require.Error(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_payment_refund_resolutions SET evidence='tampered' WHERE operation_id=$1`, cmd.OperationID)
	require.Error(t, err)
	var balance, frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, user).Scan(&balance, &frozen))
	require.True(t, balance.Equal(decimal.NewFromInt(2)))
	require.True(t, frozen.IsZero())
	require.Equal(t, 1, gateway.g.calls)
}

func TestPaymentFundingRefundManualFailedRestoresSource(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-manual-fail-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &unprovenRefundGateway{g: &refundGatewayFake{submitErr: errors.New("timeout"), queryStatus: payment.ProviderStatusSuccess}}
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	svc.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
	_, err = svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
	require.NoError(t, err)
	_, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	queue, err := svc.ListPendingFundingRefunds(ctx, 100)
	require.NoError(t, err)
	var attempt string
	for _, item := range queue {
		if item.OrderID == order.ID {
			attempt = item.AttemptID
		}
	}
	require.NotEmpty(t, attempt)
	cmd := service.UncertainFundingResolution{OrderID: order.ID, AttemptID: attempt, ActorID: 123, Decision: "confirmed_failed", ProviderRefundID: "", Evidence: "bank statement verified", OperationID: email + "-op"}
	result, err := svc.ResolveUncertainFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Success)
	result, err = svc.ResolveUncertainFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Success)
	cmd.Evidence = "changed"
	_, err = svc.ResolveUncertainFunding(ctx, cmd)
	require.Error(t, err)
	var balance, frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, user).Scan(&balance, &frozen))
	require.True(t, balance.Equal(decimal.NewFromInt(5)))
	require.True(t, frozen.IsZero())
	require.Equal(t, 1, gateway.g.calls)
}

func TestPaymentFundingRefundMismatchedProviderReferenceRemainsHeld(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-gateway-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &refundGatewayFake{submitStatus: payment.ProviderStatusPending, queryStatus: payment.ProviderStatusFailed, queryID: "different-refund"}
	newService := func() *service.PaymentService {
		s := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
		s.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
		return s
	}
	svc := newService()
	result, err := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, 1, gateway.calls)
	// A fresh service finalizes the same durable attempt after a failure query.
	svc = newService()
	result, err = svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	var balance, frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, user).Scan(&balance, &frozen))
	require.True(t, balance.Equal(decimal.NewFromInt(2)))
	require.True(t, frozen.Equal(decimal.NewFromInt(3)))
	require.Equal(t, 1, gateway.calls)
}

func TestPaymentFundingRefundLateSuccessCannotUndoManualFailure(t *testing.T) {
	ctx := context.Background()
	email := fmt.Sprintf("refund-race-gateway-%d@example.test", time.Now().UnixNano())
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, email).Scan(&user))
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(user).SetUserEmail(email).SetUserName(email).SetAmount(5).SetPayAmount(5).SetFeeRate(0).SetRechargeCode(email).SetOutTradeNo(email).SetPaymentType("stripe").SetPaymentTradeNo("fake-trade").SetOrderType("balance").SetStatus("COMPLETED").SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,5,5)`, user, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	gateway := &refundGatewayFake{submitStatus: payment.ProviderStatusSuccess, queryStatus: "UNKNOWN", entered: make(chan struct{}), resume: make(chan struct{})}
	svc := service.NewPaymentService(integrationEntClient, nil, nil, nil, nil, nil, repo, nil, nil)
	svc.SetRefundProviderResolver(func(context.Context, *dbent.PaymentOrder) (payment.Provider, error) { return gateway, nil })
	done := make(chan error, 1)
	go func() {
		_, e := svc.ExecuteRefund(ctx, &service.RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 3, Reason: "test"})
		done <- e
	}()
	<-gateway.entered
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.False(t, result.Success)
	queue, err := svc.ListPendingFundingRefunds(ctx, 100)
	require.NoError(t, err)
	var attempt string
	for _, item := range queue {
		if item.OrderID == order.ID {
			attempt = item.AttemptID
		}
	}
	require.NotEmpty(t, attempt)
	resolved, err := svc.ResolveUncertainFunding(ctx, service.UncertainFundingResolution{OrderID: order.ID, AttemptID: attempt, ActorID: 123, Decision: "confirmed_failed", Evidence: "bank confirmed failed and submitter stopped", OperationID: email + "-manual"})
	require.NoError(t, err)
	require.True(t, resolved.Success)
	close(gateway.resume)
	require.Error(t, <-done)
	var status string
	var balance, frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM payment_orders WHERE id=$1`, order.ID).Scan(&status))
	require.Equal(t, service.OrderStatusRefundFailed, status)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1`, user).Scan(&balance, &frozen))
	require.True(t, balance.Equal(decimal.NewFromInt(5)))
	require.True(t, frozen.IsZero())
}

func TestPaymentFundingRefundRejectsRestoredCashAfterChargeback(t *testing.T) {
	ctx := context.Background()
	suffix, receipt, buyer, _, admin, lot, _ := paymentRefundCaptureFixture(t)
	order, err := integrationEntClient.PaymentOrder.Create().SetUserID(buyer).SetUserEmail(suffix + "@example.test").SetUserName(suffix).SetAmount(10).SetPayAmount(10).SetFeeRate(0).SetRechargeCode(suffix).SetOutTradeNo(suffix).SetPaymentType("stripe").SetPaymentTradeNo(suffix).SetOrderType("balance").SetStatus("COMPLETED").SetPaidAt(time.Now()).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("example.test").Save(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE wallet_funding_states SET migration_version=1 WHERE user_id=$1`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE wallet_funding_lots SET origin_id=$2 WHERE id=$1`, lot, fmt.Sprint(order.ID))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.RecordPaymentChargeback(dbent.NewTxContext(ctx, tx), order.ID, buyer, admin, 10, suffix+"-chargeback", "bank evidence"))
	require.NoError(t, tx.Commit())
	sqltx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().Credit(ctx, sqltx, buyer, suffix+"-credit", "test", decimal.NewFromInt(3), service.FundingPromotional, "test", suffix))
	require.NoError(t, sqltx.Commit())
	require.NoError(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(3), suffix+"-consume-refund"))
	tx, err = integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order.ID, buyer, 3, suffix+"-payment-refund")
	require.ErrorIs(t, err, service.ErrInsufficientFunding, "a chargeback already returned the entire payment")
}

func paymentRefundCaptureFixture(t *testing.T) (string, string, int64, int64, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	s := uuid.NewString()
	receipt := uuid.NewString()
	var buyer, owner, admin, key, group, account, offer, attempt, lot, original int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',7) RETURNING id`, s+"b@test.invalid").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, s+"o@test.invalid").Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, s+"a@test.invalid").Scan(&admin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id) VALUES($1)`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id,pending) VALUES($1,2)`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, buyer)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,10,7) RETURNING id`, buyer, s).Scan(&lot))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_operations(operation_id,user_id,actor,operation_type,fingerprint) VALUES($1,$2,'test','hold','test')`, marketplaceHoldID(receipt), buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_allocations(lot_id,hold_operation_id,reserved,captured) VALUES($1,$2,3,3)`, lot, marketplaceHoldID(receipt))
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, buyer, s).Scan(&key))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, s).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, s, owner).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, account, owner).Scan(&offer))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_requests(id,buyer_user_id,api_key_id,group_id,request_digest,status) VALUES($1,$2,$3,$4,'test','settled')`, receipt, buyer, key, group)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status) VALUES($1,1,$2,$3,$4,'{}','test','settled') RETURNING id`, receipt, account, offer, owner).Scan(&attempt))
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	entries := []LedgerPosting{{Account: "marketplace_clearing", Side: "debit", Amount: decimal.NewFromInt(3)}, {Account: "supplier_pending", SubjectID: &owner, Side: "credit", Amount: decimal.NewFromInt(2)}, {Account: "platform_fee", Side: "credit", Amount: decimal.NewFromInt(1)}}
	require.NoError(t, NewMarketplaceLedgerRepository().Post(ctx, tx, "capture:"+receipt, "test", "USD", entries))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM ledger_transactions WHERE operation_id=$1`, "capture:"+receipt).Scan(&original))
	require.NoError(t, tx.Commit())
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_captures(receipt_id,attempt_id,owner_user_id,consumer_charge,supplier_net,platform_fee,matures_at,ledger_transaction_id,fingerprint,buyer_balance_after) VALUES($1,$2,$3,3,2,1,now()+interval '1 day',$4,'test',7)`, receipt, attempt, owner, original)
	require.NoError(t, err)
	return s, receipt, buyer, owner, admin, lot, original
}

func TestPaymentRefundAndChargebackSerializeAgainstOrderCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	email := uuid.NewString() + "@test.invalid"
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',10) RETURNING id`, email).Scan(&user))
	order := createRefundTestOrder(t, user, email, 10)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version) VALUES($1,1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','payment_order',$2,10,10)`, user, fmt.Sprint(order))
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	start, done := make(chan struct{}), make(chan error, 2)
	for _, isRefund := range []bool{true, false} {
		go func(refund bool) {
			<-start
			tx, e := integrationEntClient.Tx(ctx)
			if e == nil {
				if refund {
					_, e = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order, user, 6, email+"-refund")
				} else {
					e = repo.RecordPaymentChargeback(dbent.NewTxContext(ctx, tx), order, user, 1, 6, email+"-chargeback", "bank evidence")
				}
				if e == nil {
					e = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
			}
			done <- e
		}(isRefund)
	}
	close(start)
	first, second := <-done, <-done
	require.True(t, (first == nil && errors.Is(second, service.ErrInsufficientFunding)) || (second == nil && errors.Is(first, service.ErrInsufficientFunding)), "results: %v, %v", first, second)
	var total decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT coalesce((SELECT sum(amount) FROM marketplace_payment_chargebacks WHERE order_id=$1),0)+coalesce((SELECT sum(amount) FROM marketplace_payment_refund_attempts WHERE order_id=$1 AND state IN ('reserved','confirmed')),0)`, order).Scan(&total))
	require.True(t, total.Equal(decimal.NewFromInt(6)))
}
