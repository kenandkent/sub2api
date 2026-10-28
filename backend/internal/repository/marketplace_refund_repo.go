package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

type MarketplaceRefundRepository struct{ db *sql.DB }

func NewMarketplaceRefundRepository(db *sql.DB) *MarketplaceRefundRepository {
	return &MarketplaceRefundRepository{db: db}
}

// Refund atomically restores the original captured lots and reverses supplier
// earnings. The wallet lock serializes this transaction with payout start.
func (r *MarketplaceRefundRepository) Refund(ctx context.Context, receipt string, actor int64, amount decimal.Decimal, operation string) error {
	return r.refund(ctx, receipt, actor, amount, operation, "")
}

// RefundWithReason persists the administrative reason and binds it to replay.
func (r *MarketplaceRefundRepository) RefundWithReason(ctx context.Context, receipt string, actor int64, amount decimal.Decimal, operation, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 4096 {
		return service.ErrMarketplaceRefundInvalid
	}
	return r.refund(ctx, receipt, actor, amount, operation, reason)
}

func (r *MarketplaceRefundRepository) refund(ctx context.Context, receipt string, actor int64, amount decimal.Decimal, operation, reason string) error {
	if strings.TrimSpace(receipt) == "" || actor <= 0 || !amount.IsPositive() || !amount.Equal(amount.Round(8)) || strings.TrimSpace(operation) == "" || len(operation) > 255 {
		return service.ErrMarketplaceRefundInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if !lockPayoutAdmin(ctx, tx, actor) {
		return service.ErrMarketplaceRefundInvalid
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "marketplace:refund:"+operation); err != nil {
		return err
	}
	var oldReceipt, oldReason string
	var oldActor int64
	var oldAmount decimal.Decimal
	err = tx.QueryRowContext(ctx, `SELECT receipt_id,actor_user_id,amount,reason FROM marketplace_refunds WHERE operation_id=$1`, operation).Scan(&oldReceipt, &oldActor, &oldAmount, &oldReason)
	if err == nil {
		if oldReceipt != receipt || oldActor != actor || !oldAmount.Equal(amount) || oldReason != reason {
			return ErrFundingOperationConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var buyer, owner int64
	if err = tx.QueryRowContext(ctx, `SELECT r.buyer_user_id,c.owner_user_id FROM marketplace_requests r JOIN marketplace_captures c ON c.receipt_id=r.id WHERE r.id=$1`, receipt).Scan(&buyer, &owner); err != nil {
		return err
	}
	_, _, buyerDebt, _, err := fundingLock(ctx, tx, buyer)
	if err != nil {
		return err
	}
	var pending, available decimal.Decimal
	if err = tx.QueryRowContext(ctx, `SELECT pending,available FROM supplier_wallets WHERE user_id=$1 AND currency='USD' FOR UPDATE`, owner).Scan(&pending, &available); err != nil {
		return err
	}
	var frozen bool
	if err = tx.QueryRowContext(ctx, `SELECT payout_frozen FROM supplier_profiles WHERE user_id=$1 FOR UPDATE`, owner).Scan(&frozen); err != nil {
		return err
	}
	if _, err = lockMarketplaceReceipt(ctx, tx, receipt); err != nil {
		return err
	}
	var charge, net, offset, oldCharge, oldNet, oldFee decimal.Decimal
	var status string
	var original sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT consumer_charge,supplier_net,supplier_debt_offset,refunded_charge,refunded_net,refunded_fee,status,ledger_transaction_id FROM marketplace_captures WHERE receipt_id=$1 FOR UPDATE`, receipt).Scan(&charge, &net, &offset, &oldCharge, &oldNet, &oldFee, &status, &original); err != nil {
		return err
	}
	if status != "pending" && status != "matured" {
		return service.ErrMarketplaceRefundInvalid
	}
	newCharge := oldCharge.Add(amount)
	newNet, newFee, err := service.CumulativeRefund(charge, net, newCharge)
	if err != nil {
		return err
	}
	deltaNet, deltaFee := newNet.Sub(oldNet), newFee.Sub(oldFee)
	if deltaNet.IsNegative() || deltaFee.IsNegative() || !original.Valid {
		return service.ErrMarketplaceRefundInvalid
	}
	// Restore the exact captured allocation, retaining original lot provenance.
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.lot_id,a.captured-a.refunded FROM wallet_funding_allocations a JOIN wallet_funding_lots l ON l.id=a.lot_id WHERE a.hold_operation_id=$1 AND l.user_id=$2 ORDER BY a.lot_id FOR UPDATE OF a`, marketplaceHoldID(receipt), buyer)
	if err != nil {
		return err
	}
	type allocation struct {
		id, lot int64
		open    decimal.Decimal
	}
	var allocations []allocation
	for rows.Next() {
		var a allocation
		if err = rows.Scan(&a.id, &a.lot, &a.open); err != nil {
			_ = rows.Close()
			return err
		}
		allocations = append(allocations, a)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	left := amount
	for _, a := range allocations {
		part := decimal.Min(left, a.open)
		if !part.IsPositive() {
			continue
		}
		paid := decimal.Min(part, buyerDebt)
		buyerDebt = buyerDebt.Sub(paid)
		left = left.Sub(part)
		if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_lots SET remaining=remaining+$2 WHERE id=$1`, a.lot, part.Sub(paid)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_allocations SET refunded=refunded+$2 WHERE id=$1`, a.id, part); err != nil {
			return err
		}
	}
	if !left.IsZero() {
		return ErrFundingInvariant
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_states SET debt=$2 WHERE user_id=$1`, buyer, buyerDebt); err != nil {
		return err
	}
	if err = fundingUserUpdate(ctx, tx, `UPDATE users SET balance=balance+$2 WHERE id=$1`, buyer, amount); err != nil {
		return err
	}
	fromPending := decimal.Zero
	if status == "pending" {
		fromPending = decimal.Min(deltaNet, service.MaturityAmount(net, offset, oldNet))
	}
	if pending.LessThan(fromPending) {
		return ErrFundingInvariant
	}
	fromAvailable := decimal.Min(available, deltaNet.Sub(fromPending))
	debt := deltaNet.Sub(fromPending).Sub(fromAvailable)
	if _, err = tx.ExecContext(ctx, `UPDATE supplier_wallets SET pending=pending-$2,available=available-$3,debt=debt+$4,version=version+1 WHERE user_id=$1 AND currency='USD'`, owner, fromPending, fromAvailable, debt); err != nil {
		return err
	}
	if debt.IsPositive() {
		if _, err = tx.ExecContext(ctx, `UPDATE supplier_profiles SET payout_frozen=true,risk_reason='consumption refund debt',version=version+1,updated_at=now() WHERE user_id=$1`, owner); err != nil {
			return err
		}
		if err = NewSupplierPayoutRepository(r.db).CancelPreProcessingTx(ctx, tx, owner, operation, fmt.Sprintf("admin:%d", actor)); err != nil {
			return err
		}
	}
	entries := []LedgerPosting{}
	if fromPending.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "supplier_pending", SubjectID: &owner, Side: "debit", Amount: fromPending})
	}
	if fromAvailable.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "supplier_available", SubjectID: &owner, Side: "debit", Amount: fromAvailable})
	}
	if debt.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "supplier_debt", SubjectID: &owner, Side: "debit", Amount: debt})
	}
	if deltaFee.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "platform_fee", Side: "debit", Amount: deltaFee})
	}
	entries = append(entries, LedgerPosting{Account: "buyer_available", SubjectID: &buyer, Side: "credit", Amount: amount})
	op := "marketplace:refund:" + operation
	if err = NewMarketplaceLedgerRepository().PostWithOriginal(ctx, tx, op, fmt.Sprintf("admin:%d", actor), "USD", original.Int64, entries); err != nil {
		return err
	}
	var ledgerID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM ledger_transactions WHERE operation_id=$1`, op).Scan(&ledgerID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_captures SET refunded_charge=$2,refunded_net=$3,refunded_fee=$4 WHERE receipt_id=$1`, receipt, newCharge, newNet, newFee); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_refunds(operation_id,receipt_id,actor_user_id,amount,supplier_amount,platform_amount,ledger_transaction_id,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, operation, receipt, actor, amount, deltaNet, deltaFee, ledgerID, reason); err != nil {
		return err
	}
	return tx.Commit()
}
