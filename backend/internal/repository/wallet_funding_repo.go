package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

var ErrFundingOperationConflict = errors.New("funding operation id conflict")
var ErrFundingInvariant = errors.New("funding projection invariant violated")

type WalletFundingRepository struct{}

func NewWalletFundingRepository() *WalletFundingRepository { return &WalletFundingRepository{} }

// All methods require a caller-owned SQL transaction. Roll back after any error.
// Locks are acquired in the order user, funding state, lots by ID.
func fundingLock(ctx context.Context, tx *sql.Tx, user int64) (decimal.Decimal, decimal.Decimal, decimal.Decimal, []service.FundingLot, error) {
	var b, h, debt decimal.Decimal
	if tx == nil {
		return b, h, debt, nil, errors.New("funding transaction required")
	}
	if err := tx.QueryRowContext(ctx, `SELECT balance,frozen_balance FROM users WHERE id=$1 FOR NO KEY UPDATE`, user).Scan(&b, &h); err != nil {
		return b, h, debt, nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT debt FROM wallet_funding_states WHERE user_id=$1 FOR UPDATE`, user).Scan(&debt); err != nil {
		return b, h, debt, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,remaining,reserved FROM wallet_funding_lots WHERE user_id=$1 ORDER BY id FOR UPDATE`, user)
	if err != nil {
		return b, h, debt, nil, err
	}
	defer func() { _ = rows.Close() }()
	lots := []service.FundingLot{}
	total, reserved := decimal.Zero, decimal.Zero
	for rows.Next() {
		var l service.FundingLot
		if err = rows.Scan(&l.ID, &l.Kind, &l.Remaining, &l.Reserved); err != nil {
			return b, h, debt, nil, err
		}
		lots = append(lots, l)
		total = total.Add(l.Remaining)
		reserved = reserved.Add(l.Reserved)
	}
	if err = rows.Err(); err != nil {
		return b, h, debt, nil, err
	}
	if !total.Sub(debt).Equal(b.Add(h)) || !reserved.Equal(h) {
		return b, h, debt, nil, ErrFundingInvariant
	}
	return b, h, debt, lots, nil
}
func fundingOperation(ctx context.Context, tx *sql.Tx, user int64, op, actor, kind, fp string) (bool, error) {
	if op == "" || actor == "" {
		return false, errors.New("operation ID and actor required")
	}
	var u int64
	var a, k, f string
	err := tx.QueryRowContext(ctx, `SELECT user_id,actor,operation_type,fingerprint FROM wallet_funding_operations WHERE operation_id=$1`, op).Scan(&u, &a, &k, &f)
	if err == nil {
		if u != user || a != actor || k != kind || f != fp {
			return false, ErrFundingOperationConflict
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO wallet_funding_operations(operation_id,user_id,actor,operation_type,fingerprint) VALUES($1,$2,$3,$4,$5)`, op, user, actor, kind, fp)
	return false, err
}
func fundingPost(ctx context.Context, tx *sql.Tx, user int64, op, actor, debit, credit string, amount decimal.Decimal) error {
	if amount.IsZero() {
		return nil
	}
	return NewMarketplaceLedgerRepository().Post(ctx, tx, op, actor, "USD", []LedgerPosting{{Account: debit, SubjectID: &user, Side: "debit", Amount: amount}, {Account: credit, SubjectID: &user, Side: "credit", Amount: amount}})
}

// fundingUserUpdate bypasses the legacy compatibility trigger only for this
// projection update. Restore the prior transaction-local value immediately so
// later legacy writes in the caller's transaction cannot silently bypass it.
func fundingUserUpdate(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	var previous sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT current_setting('sub2api.funding_explicit', true)`).Scan(&previous); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('sub2api.funding_explicit','true',true)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `SELECT set_config('sub2api.funding_explicit',$1,true)`, previous.String)
	return err
}

func fundingAmount(amount decimal.Decimal) (decimal.Decimal, error) {
	a := service.QuantizeFunding(amount)
	if !a.IsPositive() {
		return a, service.ErrInvalidFunding
	}
	return a, nil
}

// Hold reserves eligible lots in FIFO order, refusing users with debt.
func (*WalletFundingRepository) Hold(ctx context.Context, tx *sql.Tx, user int64, op, actor string, amount decimal.Decimal) error {
	amount, err := fundingAmount(amount)
	if err != nil {
		return err
	}
	b, _, debt, lots, err := fundingLock(ctx, tx, user)
	if err != nil {
		return err
	}
	replay, err := fundingOperation(ctx, tx, user, op, actor, "hold", amount.StringFixed(8))
	if err != nil || replay {
		return err
	}
	var riskBlocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_payment_risk_flags WHERE user_id=$1)`, user).Scan(&riskBlocked); err != nil {
		return err
	}
	if riskBlocked {
		return service.ErrInsufficientFunding
	}
	if debt.IsPositive() || b.LessThan(amount) {
		return service.ErrInsufficientFunding
	}
	portions, err := service.PlanFundingConsumption(lots, amount, true)
	if err != nil {
		return err
	}
	for _, p := range portions {
		if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_lots SET reserved=reserved+$2 WHERE id=$1`, p.LotID, p.Amount); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO wallet_funding_allocations(lot_id,hold_operation_id,reserved) VALUES($1,$2,$3)`, p.LotID, op, p.Amount); err != nil {
			return err
		}
	}
	if err = fundingUserUpdate(ctx, tx, `UPDATE users SET balance=balance-$2,frozen_balance=frozen_balance+$2 WHERE id=$1`, user, amount); err != nil {
		return err
	}
	return fundingPost(ctx, tx, user, op, actor, "buyer_available", "buyer_frozen", amount)
}

// SettleHold captures cost and releases the remainder, paying existing debt first.
func (*WalletFundingRepository) SettleHold(ctx context.Context, tx *sql.Tx, user int64, op, actor, holdID string, cost decimal.Decimal) error {
	cost = service.QuantizeFunding(cost)
	if cost.IsNegative() || holdID == "" {
		return service.ErrInvalidFunding
	}
	_, _, debt, _, err := fundingLock(ctx, tx, user)
	if err != nil {
		return err
	}
	replay, err := fundingOperation(ctx, tx, user, op, actor, "settle", holdID+":"+cost.StringFixed(8))
	if err != nil || replay {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.lot_id,a.reserved-a.captured-a.released FROM wallet_funding_allocations a JOIN wallet_funding_lots l ON l.id=a.lot_id WHERE a.hold_operation_id=$1 AND l.user_id=$2 ORDER BY a.lot_id FOR UPDATE OF a`, holdID, user)
	if err != nil {
		return err
	}
	type alloc struct {
		id, lot int64
		open    decimal.Decimal
	}
	var allocations []alloc
	total := decimal.Zero
	for rows.Next() {
		var a alloc
		if err = rows.Scan(&a.id, &a.lot, &a.open); err != nil {
			_ = rows.Close()
			return err
		}
		allocations = append(allocations, a)
		total = total.Add(a.open)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if len(allocations) == 0 || total.IsZero() || cost.GreaterThan(total) {
		return service.ErrInsufficientFunding
	}
	left := cost
	release := total.Sub(cost)
	for _, a := range allocations {
		captured := decimal.Min(left, a.open)
		released := a.open.Sub(captured)
		paid, _ := service.FundingApplyDebt(released, debt)
		debt = debt.Sub(paid)
		left = left.Sub(captured)
		if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_lots SET reserved=reserved-$2, remaining=remaining-$3 WHERE id=$1`, a.lot, a.open, captured.Add(paid)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_allocations SET captured=captured+$2,released=released+$3 WHERE id=$1`, a.id, captured, released); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_states SET debt=$2 WHERE user_id=$1`, user, debt); err != nil {
		return err
	}
	if err = fundingUserUpdate(ctx, tx, `UPDATE users SET frozen_balance=frozen_balance-$2,balance=balance+$3 WHERE id=$1`, user, total, release); err != nil {
		return err
	}
	// A single balanced transaction covers both capture and release.
	entries := []LedgerPosting{{Account: "buyer_frozen", SubjectID: &user, Side: "debit", Amount: total}}
	if cost.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "marketplace_clearing", Side: "credit", Amount: cost})
	}
	if release.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "buyer_available", SubjectID: &user, Side: "credit", Amount: release})
	}
	return NewMarketplaceLedgerRepository().Post(ctx, tx, op, actor, "USD", entries)
}

// Credit first pays debt; only the remainder becomes an unreserved lot.
func (*WalletFundingRepository) Credit(ctx context.Context, tx *sql.Tx, user int64, op, actor string, amount decimal.Decimal, kind service.FundingKind, originType, originID string) error {
	amount, err := fundingAmount(amount)
	if err != nil {
		return err
	}
	if originType == "" || originID == "" {
		return service.ErrInvalidFunding
	}
	switch kind {
	case service.FundingCashPaid, service.FundingPromotional, service.FundingLegacy, service.FundingSubsidy:
	default:
		return service.ErrInvalidFunding
	}
	_, _, debt, _, err := fundingLock(ctx, tx, user)
	if err != nil {
		return err
	}
	fp := fmt.Sprintf("%q:%q:%q:%q", amount.StringFixed(8), kind, originType, originID)
	replay, err := fundingOperation(ctx, tx, user, op, actor, "credit", fp)
	if err != nil || replay {
		return err
	}
	paid, available := service.FundingApplyDebt(amount, debt)
	// Keep provenance even when all funds paid debt; zero remaining lots are valid.
	if _, err = tx.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,$2,$3,$4,$5,$6)`, user, kind, originType, originID, amount, available); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_states SET debt=debt-$2 WHERE user_id=$1`, user, paid); err != nil {
		return err
	}
	if err = fundingUserUpdate(ctx, tx, `UPDATE users SET balance=balance+$2 WHERE id=$1`, user, amount); err != nil {
		return err
	}
	return fundingPost(ctx, tx, user, op, actor, "funding_source", "buyer_available", amount)
}

// SpendSelfOwned can overdraw, but never consumes reserved lots.
func (*WalletFundingRepository) SpendSelfOwned(ctx context.Context, tx *sql.Tx, user int64, op, actor string, amount decimal.Decimal) error {
	amount, err := fundingAmount(amount)
	if err != nil {
		return err
	}
	_, _, _, lots, err := fundingLock(ctx, tx, user)
	if err != nil {
		return err
	}
	replay, err := fundingOperation(ctx, tx, user, op, actor, "self_spend", amount.StringFixed(8))
	if err != nil || replay {
		return err
	}
	available := decimal.Zero
	for _, l := range lots {
		available = available.Add(l.Remaining.Sub(l.Reserved))
	}
	use := decimal.Min(amount, available)
	portions, err := service.PlanFundingConsumption(lots, use, false)
	if err != nil {
		return err
	}
	for _, p := range portions {
		if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_lots SET remaining=remaining-$2 WHERE id=$1`, p.LotID, p.Amount); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_states SET debt=debt+$2 WHERE user_id=$1`, user, amount.Sub(use)); err != nil {
		return err
	}
	if err = fundingUserUpdate(ctx, tx, `UPDATE users SET balance=balance-$2 WHERE id=$1`, user, amount); err != nil {
		return err
	}
	return fundingPost(ctx, tx, user, op, actor, "buyer_available", "self_consumption", amount)
}
