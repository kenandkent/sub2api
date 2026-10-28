//go:build integration

package repository

import (
	"context"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type fundingAdminTestPort interface {
	ReclassifyLegacyFunding(context.Context, service.FundingReclassCommand) (*service.FundingReclassResult, error)
	ClearFundingRisk(context.Context, service.FundingRiskClearCommand) (*service.FundingRiskClearResult, error)
}

func fundingAdminFixture(t *testing.T) (int64, int64, string, fundingAdminTestPort) {
	return fundingAdminFixtureBalance(t, 25)
}
func fundingAdminFixtureBalance(t *testing.T, balance int) (int64, int64, string, fundingAdminTestPort) {
	t.Helper()
	isolateCommittedIntegrationTest(t)
	user, code := reviewUser(t, balance)
	reviewMigrate(t, user)
	var admin int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO users(email,password_hash,role,status) VALUES($1,'x','admin','active') RETURNING id`, code+"-admin@example.test").Scan(&admin))
	repo := any(newUserRepositoryWithSQL(integrationEntClient, integrationDB))
	port, ok := repo.(fundingAdminTestPort)
	require.True(t, ok, "audited funding administration is required")
	return user, admin, code, port
}
func reviewProofOfFulfillment(t *testing.T, user, order int64, code string) {
	t.Helper()
	code = strings.ReplaceAll(code, "-", "")
	_, err := integrationDB.Exec(`UPDATE payment_orders SET recharge_code=$2,paid_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, order, code)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES($1,'balance',100,'used',$2,now())`, code, user)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE payment_orders SET completed_at=now() WHERE id=$1`, order)
	require.NoError(t, err)
}
func TestFundingAdminReclassifyLegacyDoesNotMint(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(25), RemainingProvenanceConfirmed: true, Evidence: "verified paid order", OperationID: code + "-reclass"}
	result, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.CashAmount.Equal(decimal.NewFromInt(20)))
	require.True(t, result.BonusAmount.Equal(decimal.NewFromInt(5)))
	replay, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, replay.Replay)
	cmd.Evidence = "different"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err)
	var balance, total, cash, bonus decimal.Decimal
	require.NoError(t, integrationDB.QueryRow(`SELECT u.balance, sum(l.remaining),sum(l.remaining) FILTER(WHERE l.kind='cash_paid'),sum(l.remaining) FILTER(WHERE l.origin_type='payment_bonus') FROM users u JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1 GROUP BY u.balance`, user).Scan(&balance, &total, &cash, &bonus))
	require.True(t, balance.Equal(decimal.NewFromInt(25)))
	require.True(t, total.Equal(balance))
	require.True(t, cash.Equal(decimal.NewFromInt(20)))
	require.True(t, bonus.Equal(decimal.NewFromInt(5)))
	cmd.OperationID = code + "-second"
	cmd.Evidence = "verified"
	cmd.Amount = decimal.NewFromInt(1)
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err)
}
func TestFundingAdminRiskClearRequiresDebtZeroAndVersion(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	_, err := integrationDB.Exec(`INSERT INTO marketplace_payment_risk_flags(user_id,reason) VALUES($1,'chargeback')`, user)
	require.NoError(t, err)
	var version int64
	require.NoError(t, integrationDB.QueryRow(`SELECT version FROM marketplace_payment_risk_flags WHERE user_id=$1`, user).Scan(&version))
	cmd := service.FundingRiskClearCommand{UserID: user, ActorID: admin, ExpectedVersion: version + 1, Evidence: "reviewed", OperationID: code + "-clear"}
	_, err = port.ClearFundingRisk(ctx, cmd)
	require.Error(t, err)
	cmd.ExpectedVersion = version
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().SpendSelfOwned(ctx, tx, user, code+"-overspend", "test", decimal.NewFromInt(30)))
	require.NoError(t, tx.Commit())
	_, err = port.ClearFundingRisk(ctx, cmd)
	require.Error(t, err, "risk cannot clear while buyer has debt")
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().Credit(ctx, tx, user, code+"-debt-payment", "test", decimal.NewFromInt(5), service.FundingPromotional, "test", code+"-debt-payment"))
	require.NoError(t, tx.Commit())
	r, err := port.ClearFundingRisk(ctx, cmd)
	require.NoError(t, err)
	require.False(t, r.Replay)
	r, err = port.ClearFundingRisk(ctx, cmd)
	require.NoError(t, err)
	require.True(t, r.Replay)
	_, err = integrationDB.Exec(`INSERT INTO marketplace_payment_risk_flags(user_id,reason) VALUES($1,'new chargeback')`, user)
	require.NoError(t, err)
	cmd.OperationID = code + "-clear-new"
	_, err = port.ClearFundingRisk(ctx, cmd)
	require.Error(t, err, "old version cannot clear new risk")
}

func TestFundingAdminReclassifyCapsHistoricalReversalAndVerifiesOwner(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "PARTIALLY_REFUNDED", 80, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(21), RemainingProvenanceConfirmed: true, Evidence: "bank statement", OperationID: code + "-excess"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err)
	cmd.Amount = decimal.NewFromInt(20)
	cmd.OperationID = code + "-valid"
	result, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.CashAmount.Equal(decimal.NewFromInt(16)))
	require.True(t, result.BonusAmount.Equal(decimal.NewFromInt(4)))
	cmd.UserID = admin
	cmd.OperationID = code + "-owner"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err)
}

func TestFundingAdminReclassifyOnlyUnreservedLegacy(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT set_config('sub2api.funding_explicit','true',true)`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE wallet_funding_lots SET reserved=20 WHERE user_id=$1 AND kind='legacy_unverified'`, user)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=5,frozen_balance=20 WHERE id=$1`, user)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(6), RemainingProvenanceConfirmed: true, Evidence: "verified", OperationID: code + "-too-large"}
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err)
	cmd.Amount = decimal.NewFromInt(5)
	cmd.OperationID = code + "-allowed"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	var frozen, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRow(`SELECT u.frozen_balance,l.reserved FROM users u JOIN wallet_funding_lots l ON l.user_id=u.id WHERE u.id=$1 AND l.kind='legacy_unverified'`, user).Scan(&frozen, &reserved))
	require.True(t, frozen.Equal(decimal.NewFromInt(20)))
	require.True(t, reserved.Equal(frozen))
}
func TestFundingAdminConcurrentReclassifyCannotExceedAvailable(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	errs := make(chan error, 2)
	for _, suffix := range []string{"a", "b"} {
		go func(suffix string) {
			_, err := port.ReclassifyLegacyFunding(ctx, service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(20), RemainingProvenanceConfirmed: true, Evidence: "verified", OperationID: code + suffix})
			errs <- err
		}(suffix)
	}
	first, second := <-errs, <-errs
	require.True(t, (first == nil) != (second == nil), "one succeeds, one is rejected: %v / %v", first, second)
	var total, classified decimal.Decimal
	require.NoError(t, integrationDB.QueryRow(`SELECT sum(remaining),sum(remaining) FILTER(WHERE kind='cash_paid') FROM wallet_funding_lots WHERE user_id=$1`, user).Scan(&total, &classified))
	require.True(t, total.Equal(decimal.NewFromInt(25)))
	require.True(t, classified.Equal(decimal.NewFromInt(16)))
}

func TestFundingAdminReclassifyRejectsUncreditedOrder(t *testing.T) {
	for _, status := range []string{"PAID", "RECHARGING", "PARTIALLY_REFUNDED", "COMPLETED"} {
		t.Run(status, func(t *testing.T) {
			user, admin, code, port := fundingAdminFixture(t)
			ctx := context.Background()
			code = strings.ReplaceAll(code, "-", "")
			order := reviewCreateOrder(t, user, status, 0, code)
			cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(10), RemainingProvenanceConfirmed: true, Evidence: "paid receipt", OperationID: code + "-uncredited"}
			_, err := port.ReclassifyLegacyFunding(ctx, cmd)
			if err == nil {
				t.Error("uncredited order must not be reclassified")
				return
			}
			var count int
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM wallet_funding_lots WHERE origin_type IN ('payment_order','payment_bonus') AND origin_id=$1::text`, order).Scan(&count))
			require.Zero(t, count)
			if status == "PAID" || status == "RECHARGING" {
				// Exercise the actual fulfillment service, including the normal
				// lease retry and atomic redeem/credit transaction.
				if status == "RECHARGING" {
					_, err = integrationDB.Exec(`UPDATE payment_orders SET updated_at=now()-interval '1 hour' WHERE id=$1`, order)
					require.NoError(t, err)
				}
				users := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
				redeem := service.NewRedeemService(NewRedeemCodeRepository(integrationEntClient), users, nil, nil, nil, integrationEntClient, nil, nil)
				payments := service.NewPaymentService(integrationEntClient, nil, nil, redeem, nil, nil, users, nil, nil)
				require.NoError(t, payments.ExecuteBalanceFulfillment(ctx, order))
				require.NoError(t, payments.ExecuteBalanceFulfillment(ctx, order), "retry must not double credit")
				require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM wallet_funding_lots WHERE origin_type IN ('payment_order','payment_bonus') AND origin_id=$1::text`, order).Scan(&count))
				require.Equal(t, 2, count)
				var balance decimal.Decimal
				require.NoError(t, integrationDB.QueryRow(`SELECT balance FROM users WHERE id=$1`, user).Scan(&balance))
				require.True(t, balance.Equal(decimal.NewFromInt(125)))
			}
		})
	}
}
func TestFundingAdminReclassifyAfterModernRefundDoesNotDoubleCount(t *testing.T) {
	user, admin, code, port := fundingAdminFixtureBalance(t, 100)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(20), RemainingProvenanceConfirmed: true, Evidence: "verified", OperationID: code + "-first"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	_, err = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order, user, 10, code+"-refund")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	tx, err = integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.SettlePaymentRefund(dbent.NewTxContext(ctx, tx), code+"-refund", true))
	require.NoError(t, tx.Commit())
	var refundCash, refundBonus decimal.Decimal
	require.NoError(t, integrationDB.QueryRow(`SELECT cash_amount,bonus_amount FROM marketplace_refund_reclass_attributions WHERE attempt_id=$1`, code+"-refund").Scan(&refundCash, &refundBonus))
	require.True(t, refundCash.Equal(decimal.NewFromInt(8)))
	require.True(t, refundBonus.Equal(decimal.NewFromInt(2)))
	_, err = integrationDB.Exec(`UPDATE marketplace_refund_reclass_attributions SET cash_amount=0 WHERE attempt_id=$1`, code+"-refund")
	require.Error(t, err, "reversal attribution must be immutable")
	_, err = integrationDB.Exec(`UPDATE payment_orders SET status='PARTIALLY_REFUNDED',refund_amount=10 WHERE id=$1`, order)
	require.NoError(t, err)
	cmd.Amount = decimal.NewFromInt(80)
	cmd.OperationID = code + "-rest"
	out, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, out.CashAmount.Equal(decimal.NewFromInt(64)))
	require.True(t, out.BonusAmount.Equal(decimal.NewFromInt(16)))
	var cash, bonus, total decimal.Decimal
	require.NoError(t, integrationDB.QueryRow(`SELECT sum(remaining) FILTER(WHERE origin_type='payment_order'),sum(remaining) FILTER(WHERE origin_type='payment_bonus'),sum(remaining) FROM wallet_funding_lots WHERE user_id=$1`, user).Scan(&cash, &bonus, &total))
	require.True(t, cash.Equal(decimal.NewFromInt(72)))
	require.True(t, bonus.Equal(decimal.NewFromInt(18)))
	require.True(t, total.Equal(decimal.NewFromInt(90)))
}
func TestFundingAdminReclassifyAfterChargebackCountsOnlyUnclassifiedPart(t *testing.T) {
	user, admin, code, port := fundingAdminFixtureBalance(t, 100)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(20), RemainingProvenanceConfirmed: true, Evidence: "verified", OperationID: code + "-first"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.NoError(t, reviewCharge(t, order, 30, code+"-chargeback"))
	cmd.Amount = decimal.NewFromInt(71)
	cmd.OperationID = code + "-excess"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err)
	cmd.Amount = decimal.NewFromInt(70)
	cmd.OperationID = code + "-allowed"
	out, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, out.CashAmount.Equal(decimal.NewFromInt(56)))
	require.True(t, out.BonusAmount.Equal(decimal.NewFromInt(14)))
}

func TestFundingAdminRetainsHistoricalRefundWhenNewRefundMirrorsOrder(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "PARTIALLY_REFUNDED", 80, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(10), RemainingProvenanceConfirmed: true, Evidence: "verified", OperationID: code + "-first"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
	tx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	_, err = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order, user, 5, code+"-refund")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	tx, err = integrationEntClient.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, repo.SettlePaymentRefund(dbent.NewTxContext(ctx, tx), code+"-refund", true))
	require.NoError(t, tx.Commit())
	_, err = integrationDB.Exec(`UPDATE payment_orders SET status='PARTIALLY_REFUNDED',refund_amount=5 WHERE id=$1`, order)
	require.NoError(t, err)
	cmd.Amount = decimal.NewFromInt(11)
	cmd.OperationID = code + "-excess"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err)
	cmd.Amount = decimal.NewFromInt(10)
	cmd.OperationID = code + "-allowed"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.Error(t, reviewCharge(t, order, 16, code+"-chargeback-excess"))
	require.NoError(t, reviewCharge(t, order, 15, code+"-chargeback-allowed"))
}

func TestFundingAdminConcurrentRefundAndReclassifyDoNotDoubleCount(t *testing.T) {
	user, admin, code, port := fundingAdminFixtureBalance(t, 100)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(20), RemainingProvenanceConfirmed: true, Evidence: "verified", OperationID: code + "-first"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	errs := make(chan error, 2)
	go func() {
		cmd.Amount = decimal.NewFromInt(80)
		cmd.OperationID = code + "-rest"
		_, e := port.ReclassifyLegacyFunding(ctx, cmd)
		errs <- e
	}()
	go func() {
		repo := newUserRepositoryWithSQL(integrationEntClient, integrationDB)
		tx, e := integrationEntClient.Tx(ctx)
		if e != nil {
			errs <- e
			return
		}
		_, e = repo.ReservePaymentRefund(dbent.NewTxContext(ctx, tx), order, user, 10, code+"-refund")
		if e != nil {
			_ = tx.Rollback()
			errs <- e
			return
		}
		if e = tx.Commit(); e != nil {
			errs <- e
			return
		}
		tx, e = integrationEntClient.Tx(ctx)
		if e != nil {
			errs <- e
			return
		}
		e = repo.SettlePaymentRefund(dbent.NewTxContext(ctx, tx), code+"-refund", true)
		if e != nil {
			_ = tx.Rollback()
			errs <- e
			return
		}
		errs <- tx.Commit()
	}()
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	var balance, total, classified decimal.Decimal
	require.NoError(t, integrationDB.QueryRow(`SELECT u.balance,(SELECT sum(remaining) FROM wallet_funding_lots WHERE user_id=u.id),(SELECT sum(amount) FROM marketplace_funding_admin_operations WHERE kind='reclassify' AND order_id=$2) FROM users u WHERE id=$1`, user, order).Scan(&balance, &total, &classified))
	require.True(t, balance.Equal(decimal.NewFromInt(90)))
	require.True(t, total.Equal(balance))
	require.True(t, classified.Equal(decimal.NewFromInt(100)))
}

func TestFundingAdminReclassifyRequiresExplicitCurrentProvenance(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(10), Evidence: "paid receipt only", OperationID: code + "-no-current-evidence"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err, "payment proof alone does not prove remaining source")
	cmd.RemainingProvenanceConfirmed = true
	cmd.Evidence = "reviewed current unspent allocation"
	cmd.OperationID = code + "-verified"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	var attested, timestampsOK bool
	require.NoError(t, integrationDB.QueryRow(`SELECT remaining_provenance_confirmed,verified_paid_at<=verified_redeemed_at AND verified_redeemed_at<=verified_completed_at FROM marketplace_funding_admin_operations WHERE operation_id=$1`, cmd.OperationID).Scan(&attested, &timestampsOK))
	require.True(t, attested)
	require.True(t, timestampsOK)
	_, err = integrationDB.Exec(`UPDATE marketplace_funding_redeem_bindings SET order_id=order_id WHERE order_id=$1`, order)
	require.Error(t, err, "proof binding must be immutable")
}
func TestFundingAdminRejectsAmbiguousRedeemAndReusedProof(t *testing.T) {
	user, admin, code, port := fundingAdminFixtureBalance(t, 100)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(10), RemainingProvenanceConfirmed: true, Evidence: "verified unspent allocation", OperationID: code + "-first"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	cmd.OperationID = code + "-same-order"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err, "separate portions of the same order can use one proof")
	other := reviewCreateOrder(t, user, "COMPLETED", 0, code+"-other")
	_, err = integrationDB.Exec(`UPDATE payment_orders SET recharge_code=$2,completed_at=now() WHERE id=$1`, other, strings.ReplaceAll(code, "-", ""))
	require.NoError(t, err)
	cmd.OrderID = other
	cmd.OperationID = code + "-reuse"
	_, err = port.ReclassifyLegacyFunding(ctx, cmd)
	require.Error(t, err, "a used redeem proof cannot back two orders")
}
func TestFundingAdminRejectsAmbiguousHistoricalOrderAndInvalidProofTimes(t *testing.T) {
	for _, tc := range []string{"duplicate", "paid_after_redeem", "redeem_after_completion"} {
		t.Run(tc, func(t *testing.T) {
			user, admin, code, port := fundingAdminFixture(t)
			ctx := context.Background()
			order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
			reviewProofOfFulfillment(t, user, order, code)
			switch tc {
			case "duplicate":
				other := reviewCreateOrder(t, user, "COMPLETED", 0, code+"-other")
				_, err := integrationDB.Exec(`UPDATE payment_orders SET recharge_code=$2,completed_at=now() WHERE id=$1`, other, strings.ReplaceAll(code, "-", ""))
				require.NoError(t, err)
			case "paid_after_redeem":
				_, err := integrationDB.Exec(`UPDATE payment_orders SET paid_at=completed_at+interval '1 minute' WHERE id=$1`, order)
				require.NoError(t, err)
			case "redeem_after_completion":
				_, err := integrationDB.Exec(`UPDATE redeem_codes SET used_at=used_at+interval '1 minute' WHERE code=$1`, strings.ReplaceAll(code, "-", ""))
				require.NoError(t, err)
			}
			cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(1), RemainingProvenanceConfirmed: true, Evidence: "reviewed current unspent allocation", OperationID: code + "-ambiguous"}
			_, err := port.ReclassifyLegacyFunding(ctx, cmd)
			require.Error(t, err)
		})
	}
}

func TestFundingAdminProofFixtureUsesCausalTimeline(t *testing.T) {
	user, _, code, _ := fundingAdminFixture(t)
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	var paid, redeemed, completed, dbNow time.Time
	require.NoError(t, integrationDB.QueryRow(`SELECT p.paid_at,r.used_at,p.completed_at,clock_timestamp() FROM payment_orders p JOIN redeem_codes r ON r.code=p.recharge_code WHERE p.id=$1`, order).Scan(&paid, &redeemed, &completed, &dbNow))
	hostNow := time.Now()
	t.Logf("paid=%s redeemed=%s completed=%s db_now=%s host_now=%s host_minus_db=%s", paid.Format(time.RFC3339Nano), redeemed.Format(time.RFC3339Nano), completed.Format(time.RFC3339Nano), dbNow.Format(time.RFC3339Nano), hostNow.Format(time.RFC3339Nano), hostNow.Sub(dbNow))
	require.False(t, redeemed.Before(paid))
	require.False(t, redeemed.After(completed))
}

func TestFundingAdminReplayBeforeChangedOrderValidation(t *testing.T) {
	user, admin, code, port := fundingAdminFixture(t)
	ctx := context.Background()
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	reviewProofOfFulfillment(t, user, order, code)
	cmd := service.FundingReclassCommand{UserID: user, OrderID: order, ActorID: admin, Amount: decimal.NewFromInt(1), RemainingProvenanceConfirmed: true, Evidence: "verified", OperationID: code + "-replay"}
	_, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE payment_orders SET order_type='subscription' WHERE id=$1`, order)
	require.NoError(t, err)
	result, err := port.ReclassifyLegacyFunding(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Replay)
}
