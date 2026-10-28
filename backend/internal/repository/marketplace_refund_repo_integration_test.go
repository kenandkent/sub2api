//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func marketplaceRefundFixture(t *testing.T) (string, string, int64, int64, int64, int64, int64) {
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
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_captures(receipt_id,attempt_id,owner_user_id,consumer_charge,supplier_net,platform_fee,matures_at,ledger_transaction_id,fingerprint) VALUES($1,$2,$3,3,2,1,now()+interval '1 day',$4,'test')`, receipt, attempt, owner, original)
	require.NoError(t, err)
	return s, receipt, buyer, owner, admin, lot, original
}

func TestMarketplaceRefundPreservesFundingAndClosesRoundingTail(t *testing.T) {
	ctx := context.Background()
	s, receipt, buyer, owner, admin, lot, original := marketplaceRefundFixture(t)
	repo := NewMarketplaceRefundRepository(integrationDB)
	for i := 0; i < 3; i++ {
		op := fmt.Sprintf("%s:%d", s, i)
		require.NoError(t, repo.Refund(ctx, receipt, admin, decimal.NewFromInt(1), op))
		require.NoError(t, repo.Refund(ctx, receipt, admin, decimal.NewFromInt(1), op))
	}
	require.Error(t, repo.Refund(ctx, receipt, admin, decimal.NewFromInt(1), s+":over"))
	var balance, remaining, refunded, net, fee, pending, kind string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance::text,l.remaining::text,a.refunded::text,c.refunded_net::text,c.refunded_fee::text,w.pending::text,l.kind FROM users u JOIN wallet_funding_lots l ON l.id=$2 JOIN wallet_funding_allocations a ON a.lot_id=l.id JOIN marketplace_captures c ON c.receipt_id=$3 JOIN supplier_wallets w ON w.user_id=$4 WHERE u.id=$1`, buyer, lot, receipt, owner).Scan(&balance, &remaining, &refunded, &net, &fee, &pending, &kind))
	require.Equal(t, "10.00000000", balance)
	require.Equal(t, "10.00000000", remaining)
	require.Equal(t, "3.00000000", refunded)
	require.Equal(t, "2.00000000", net)
	require.Equal(t, "1.00000000", fee)
	require.Equal(t, "0.00000000", pending)
	require.Equal(t, "cash_paid", kind)
	var refs int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM ledger_transactions WHERE original_transaction_id=$1`, original).Scan(&refs))
	require.Equal(t, 3, refs)
}

// refundPayoutFixture matures the captured earnings, then reserves and approves
// them through the production payout state machine.
func refundPayoutFixture(t *testing.T) (string, string, int64, int64, *SupplierPayoutRepository, *service.SupplierPayout) {
	t.Helper()
	ctx := context.Background()
	s, receipt, _, owner, admin, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE supplier_profiles SET status='active' WHERE user_id=$1`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET matures_at=now()-interval '1 second' WHERE receipt_id=$1`, receipt)
	require.NoError(t, err)
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT id FROM marketplace_captures WHERE receipt_id=$1`, receipt).Scan(&id))
	ok, err := NewSupplierSettlementRepository(integrationDB).MatureCapture(ctx, id, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	repo := NewSupplierPayoutRepository(integrationDB)
	env, err := service.NewCredentialEnvelope("k", map[string][]byte{"k": make([]byte, 32)})
	require.NoError(t, err)
	dest, err := repo.CreateDestination(ctx, owner, "ending 1234", []byte("secret"), s+":dest", env)
	require.NoError(t, err)
	_, err = repo.VerifyDestination(ctx, dest.ID, admin, s+":verify")
	require.NoError(t, err)
	payout, err := repo.Request(ctx, owner, dest.ID, decimal.NewFromInt(2), s+":request")
	require.NoError(t, err)
	payout, err = repo.Action(ctx, service.PayoutAction{PayoutID: payout.ID, ActorID: admin, OperationID: s + ":approve", Action: "approve"})
	require.NoError(t, err)
	return s, receipt, owner, admin, repo, payout
}

func TestMarketplaceRefundAfterPaidCreatesDebt(t *testing.T) {
	ctx := context.Background()
	s, receipt, owner, admin, payouts, payout := refundPayoutFixture(t)
	_, err := payouts.Action(ctx, service.PayoutAction{PayoutID: payout.ID, ActorID: admin, OperationID: s + ":start", Action: "start"})
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = payouts.Action(ctx, service.PayoutAction{PayoutID: payout.ID, ActorID: admin, OperationID: s + ":confirm", Action: "confirm", Evidence: "bank verified", ExternalReference: s + ":bank", Currency: "USD", Amount: decimal.NewFromInt(2), PaidAt: &now})
	require.NoError(t, err)
	require.NoError(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(3), s+":refund"))
	var debt decimal.Decimal
	var frozen bool
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT w.debt,p.payout_frozen,x.status FROM supplier_wallets w JOIN supplier_profiles p ON p.user_id=w.user_id JOIN supplier_payouts x ON x.id=$2 WHERE w.user_id=$1`, owner, payout.ID).Scan(&debt, &frozen, &status))
	require.True(t, debt.Equal(decimal.NewFromInt(2)))
	require.True(t, frozen)
	require.Equal(t, "paid", status)
}

func TestMarketplaceRefundCompetesWithPayoutStart(t *testing.T) {
	ctx := context.Background()
	s, receipt, owner, admin, payouts, payout := refundPayoutFixture(t)
	var wg sync.WaitGroup
	wg.Add(2)
	var refundErr, startErr error
	go func() {
		defer wg.Done()
		refundErr = NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(3), s+":refund")
	}()
	go func() {
		defer wg.Done()
		_, startErr = payouts.Action(ctx, service.PayoutAction{PayoutID: payout.ID, ActorID: admin, OperationID: s + ":start", Action: "start"})
	}()
	wg.Wait()
	require.NoError(t, refundErr)
	var debt, reserved decimal.Decimal
	var frozen bool
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT w.debt,w.payout_reserved,p.payout_frozen,x.status FROM supplier_wallets w JOIN supplier_profiles p ON p.user_id=w.user_id JOIN supplier_payouts x ON x.id=$2 WHERE w.user_id=$1`, owner, payout.ID).Scan(&debt, &reserved, &frozen, &status))
	require.True(t, frozen)
	switch status {
	case "processing":
		require.NoError(t, startErr)
		require.True(t, debt.Equal(decimal.NewFromInt(2)))
		require.True(t, reserved.Equal(decimal.NewFromInt(2)))
	case "risk_cancelled":
		require.Error(t, startErr)
		require.True(t, debt.IsZero())
		require.True(t, reserved.IsZero())
	default:
		t.Fatalf("unexpected payout status %s", status)
	}
}

func TestMarketplaceRefundRepaysBuyerDebtBeforeRestoringLot(t *testing.T) {
	ctx := context.Background()
	s, receipt, buyer, _, admin, lot, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE wallet_funding_states SET debt=2 WHERE user_id=$1`, buyer)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, fundingUserUpdate(ctx, tx, `UPDATE users SET balance=5 WHERE id=$1`, buyer))
	require.NoError(t, tx.Commit())
	require.NoError(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(3), s+":refund"))
	var balance, remaining, debt decimal.Decimal
	var kind string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance,l.remaining,f.debt,l.kind FROM users u JOIN wallet_funding_lots l ON l.id=$2 JOIN wallet_funding_states f ON f.user_id=u.id WHERE u.id=$1`, buyer, lot).Scan(&balance, &remaining, &debt, &kind))
	require.True(t, balance.Equal(decimal.NewFromInt(8)))
	require.True(t, remaining.Equal(decimal.NewFromInt(8)))
	require.True(t, debt.IsZero())
	require.Equal(t, "cash_paid", kind)
}

func TestMarketplaceRefundNeverReversesAnotherCapturesPending(t *testing.T) {
	ctx := context.Background()
	s, receipt, _, owner, admin, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET supplier_debt_offset=1 WHERE receipt_id=$1`, receipt)
	require.NoError(t, err)
	// Five units of pending include four from unrelated captures.
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_wallets SET pending=5 WHERE user_id=$1`, owner)
	require.NoError(t, err)
	require.NoError(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(3), s+":refund"))
	var pending, debt decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pending,debt FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&pending, &debt))
	require.True(t, pending.Equal(decimal.NewFromInt(4)))
	require.True(t, debt.Equal(decimal.NewFromInt(1)))
}

func TestMarketplaceRefundRejectsNonAdminChangedReplayAndZeroCharge(t *testing.T) {
	ctx := context.Background()
	s, receipt, buyer, _, admin, _, _ := marketplaceRefundFixture(t)
	repo := NewMarketplaceRefundRepository(integrationDB)
	require.Error(t, repo.Refund(ctx, receipt, buyer, decimal.NewFromInt(1), s+":unauthorized"))
	require.NoError(t, repo.Refund(ctx, receipt, admin, decimal.NewFromInt(1), s+":replay"))
	require.ErrorIs(t, repo.Refund(ctx, receipt, admin, decimal.NewFromInt(2), s+":replay"), ErrFundingOperationConflict)
	_, zeroReceipt, _, _, zeroAdmin, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET consumer_charge=0,supplier_net=0,platform_fee=0,ledger_transaction_id=NULL WHERE receipt_id=$1`, zeroReceipt)
	require.NoError(t, err)
	require.Error(t, repo.Refund(ctx, zeroReceipt, zeroAdmin, decimal.NewFromInt(1), s+":zero"))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_refunds WHERE receipt_id=$1`, zeroReceipt).Scan(&count))
	require.Zero(t, count)
}

func TestPartialRefundThenMaturityMovesOnlyUnrefundedPending(t *testing.T) {
	ctx := context.Background()
	s, receipt, _, owner, admin, _, _ := marketplaceRefundFixture(t)
	refund := NewMarketplaceRefundRepository(integrationDB)
	require.NoError(t, refund.Refund(ctx, receipt, admin, decimal.NewFromInt(1), s+":first"))
	_, err := integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET matures_at=now()-interval '1 second' WHERE receipt_id=$1`, receipt)
	require.NoError(t, err)
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT id FROM marketplace_captures WHERE receipt_id=$1`, receipt).Scan(&id))
	ok, err := NewSupplierSettlementRepository(integrationDB).MatureCapture(ctx, id, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	var pending, available decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pending,available FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&pending, &available))
	require.True(t, pending.IsZero())
	require.True(t, available.Equal(decimal.RequireFromString("1.33333333")))
	require.NoError(t, refund.Refund(ctx, receipt, admin, decimal.NewFromInt(2), s+":second"))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pending,available FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&pending, &available))
	require.True(t, pending.IsZero())
	require.True(t, available.IsZero())
}

func TestMarketplaceRefundRejectsReversedCapture(t *testing.T) {
	ctx := context.Background()
	s, receipt, _, _, admin, _, _ := marketplaceRefundFixture(t)
	_, err := integrationDB.ExecContext(ctx, `UPDATE marketplace_captures SET status='reversed' WHERE receipt_id=$1`, receipt)
	require.NoError(t, err)
	require.Error(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(1), s+":refund"))
}

func TestMarketplaceRefundPreservesProcessingPayoutReservation(t *testing.T) {
	ctx := context.Background()
	s, receipt, owner, admin, payouts, payout := refundPayoutFixture(t)
	_, err := payouts.Action(ctx, service.PayoutAction{PayoutID: payout.ID, ActorID: admin, OperationID: s + ":start", Action: "start"})
	require.NoError(t, err)
	require.NoError(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(3), s+":refund"))
	var debt, reserved decimal.Decimal
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT w.debt,w.payout_reserved,p.status FROM supplier_wallets w JOIN supplier_payouts p ON p.id=$2 WHERE w.user_id=$1`, owner, payout.ID).Scan(&debt, &reserved, &status))
	require.Equal(t, "processing", status)
	require.True(t, debt.Equal(decimal.NewFromInt(2)))
	require.True(t, reserved.Equal(decimal.NewFromInt(2)))
}

func TestMarketplaceRefundCancelsApprovedPayoutBeforeStart(t *testing.T) {
	ctx := context.Background()
	s, receipt, owner, admin, payouts, payout := refundPayoutFixture(t)
	require.NoError(t, NewMarketplaceRefundRepository(integrationDB).Refund(ctx, receipt, admin, decimal.NewFromInt(3), s+":refund"))
	_, err := payouts.Action(ctx, service.PayoutAction{PayoutID: payout.ID, ActorID: admin, OperationID: s + ":start", Action: "start"})
	require.Error(t, err)
	var debt, reserved decimal.Decimal
	var status string
	var frozen bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT w.debt,w.payout_reserved,p.status,sp.payout_frozen FROM supplier_wallets w JOIN supplier_payouts p ON p.id=$2 JOIN supplier_profiles sp ON sp.user_id=w.user_id WHERE w.user_id=$1`, owner, payout.ID).Scan(&debt, &reserved, &status, &frozen))
	require.Equal(t, "risk_cancelled", status)
	require.True(t, debt.IsZero())
	require.True(t, reserved.IsZero())
	require.True(t, frozen)
}
