package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

func (r *userRepository) fundingAdminDB() (*sql.DB, error) {
	db, ok := r.sql.(*sql.DB)
	if !ok || db == nil {
		return nil, errors.New("funding administration database unavailable")
	}
	return db, nil
}
func validFundingAdmin(actor, user int64, op, evidence string) bool {
	return actor > 0 && user > 0 && strings.TrimSpace(op) != "" && len(op) <= 255 && strings.TrimSpace(evidence) != "" && len(evidence) <= 4000
}
func lockFundingAdminOperation(ctx context.Context, tx *sql.Tx, op, kind, fp string) (bool, error) {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "funding-admin:"+op); err != nil {
		return false, err
	}
	var oldKind, oldFP string
	err := tx.QueryRowContext(ctx, `SELECT kind,fingerprint FROM marketplace_funding_admin_operations WHERE operation_id=$1`, op).Scan(&oldKind, &oldFP)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if oldKind != kind || oldFP != fp {
		return false, service.ErrFundingAdminConflict
	}
	return true, nil
}

// lockFundingAdminUsers locks both principals in a global order and keeps the
// administrator's role stable through commit. Payment orders are locked first
// by reclassification, matching payment refund's order-before-buyer order.
func lockFundingAdminUsers(ctx context.Context, tx *sql.Tx, actor, user int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,role,status,deleted_at IS NULL FROM users WHERE id IN ($1,$2) ORDER BY id FOR NO KEY UPDATE`, actor, user)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	foundActor, foundUser, allowed := false, false, false
	for rows.Next() {
		var id int64
		var role, status string
		var live bool
		if err = rows.Scan(&id, &role, &status, &live); err != nil {
			return err
		}
		if id == actor {
			foundActor = true
			allowed = live && role == "admin" && status == "active"
		}
		if id == user {
			foundUser = true
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !foundActor || !allowed {
		return service.ErrFundingAdminAuthorization
	}
	if !foundUser {
		return service.ErrInvalidFunding
	}
	return nil
}

func (r *userRepository) ReclassifyLegacyFunding(ctx context.Context, cmd service.FundingReclassCommand) (*service.FundingReclassResult, error) {
	if !validFundingAdmin(cmd.ActorID, cmd.UserID, cmd.OperationID, cmd.Evidence) || cmd.OrderID <= 0 || !cmd.RemainingProvenanceConfirmed || !cmd.Amount.IsPositive() || !cmd.Amount.Equal(cmd.Amount.Round(8)) {
		return nil, service.ErrInvalidFunding
	}
	db, err := r.fundingAdminDB()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize and inspect the operation first. Replays must be resolved before
	// checking mutable order state, otherwise a committed operation can appear
	// to have been rejected after its order changes.
	fp := payoutFingerprint([]any{"reclassify", cmd.ActorID, cmd.UserID, cmd.OrderID, cmd.Amount.StringFixed(8), cmd.Evidence, cmd.RemainingProvenanceConfirmed})
	replay, opErr := lockFundingAdminOperation(ctx, tx, cmd.OperationID, "reclassify", fp)
	if opErr != nil && !errors.Is(opErr, service.ErrFundingAdminConflict) {
		return nil, opErr
	}
	result := &service.FundingReclassResult{UserID: cmd.UserID, OrderID: cmd.OrderID, Amount: cmd.Amount, OperationID: cmd.OperationID, Replay: replay}
	if replay || opErr != nil {
		if err = lockFundingAdminUsers(ctx, tx, cmd.ActorID, cmd.UserID); err != nil {
			return nil, err
		}
		if opErr != nil {
			return nil, opErr
		}
		err = tx.QueryRowContext(ctx, `SELECT cash_amount,bonus_amount FROM marketplace_funding_admin_operations WHERE operation_id=$1`, cmd.OperationID).Scan(&result.CashAmount, &result.BonusAmount)
		if err != nil {
			return nil, err
		}
		return result, tx.Commit()
	}
	// For a new operation, payment order comes before users, matching refund;
	// actor and buyer rows are acquired in a single global order.
	var amount, bonus decimal.Decimal
	var status, redeemCode string
	var paid, completed sql.NullTime
	var owner int64
	err = tx.QueryRowContext(ctx, `SELECT user_id,amount,bonus_amount,status,paid_at,completed_at,recharge_code FROM payment_orders WHERE id=$1 AND order_type='balance' FOR UPDATE`, cmd.OrderID).Scan(&owner, &amount, &bonus, &status, &paid, &completed, &redeemCode)
	if err != nil {
		return nil, err
	}
	if err = lockFundingAdminUsers(ctx, tx, cmd.ActorID, cmd.UserID); err != nil {
		return nil, err
	}
	if owner != cmd.UserID || !paid.Valid || !completed.Valid || strings.TrimSpace(redeemCode) == "" || !amount.IsPositive() || bonus.IsNegative() || bonus.GreaterThan(amount) {
		return nil, service.ErrInvalidFunding
	}
	switch status {
	case "COMPLETED", "PARTIALLY_REFUNDED", "REFUNDED":
	default:
		return nil, service.ErrInvalidFunding
	}
	// A paid order may not yet have reached the wallet. Only the used redeem
	// for this owner and amount proves the credit; an absent or inconsistent
	// historical trail must be reviewed rather than inferred from payment alone.
	var redeemID int64
	var redeemedAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT id,used_at FROM redeem_codes WHERE code=$1 AND type='balance' AND status='used' AND used_by=$2 AND value=$3 AND used_at IS NOT NULL FOR SHARE`, redeemCode, cmd.UserID, amount).Scan(&redeemID, &redeemedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrInvalidFunding
	}
	if err != nil {
		return nil, err
	}
	if redeemedAt.Before(paid.Time) || redeemedAt.After(completed.Time) {
		return nil, service.ErrInvalidFunding
	}
	var ambiguous bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM payment_orders WHERE recharge_code=$1 AND id<>$2 AND order_type='balance')`, redeemCode, cmd.OrderID).Scan(&ambiguous); err != nil {
		return nil, err
	}
	if ambiguous {
		return nil, service.ErrInvalidFunding
	}
	// A binding is durable and immutable: an imported duplicate order cannot
	// later use this same redeem as proof, even if it appears concurrently.
	if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_funding_redeem_bindings(redeem_id,order_id,user_id,actor_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, redeemID, cmd.OrderID, cmd.UserID, cmd.ActorID); err != nil {
		return nil, err
	}
	var boundOrder, boundUser int64
	err = tx.QueryRowContext(ctx, `SELECT order_id,user_id FROM marketplace_funding_redeem_bindings WHERE redeem_id=$1 FOR SHARE`, redeemID).Scan(&boundOrder, &boundUser)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrInvalidFunding
	}
	if err != nil {
		return nil, err
	}
	if boundOrder != cmd.OrderID || boundUser != cmd.UserID {
		return nil, service.ErrInvalidFunding
	}
	// Admin attests, with evidence, that the current unreserved legacy funds
	// remain attributable to this order. Historical proof alone cannot infer it.
	_, _, _, lots, err := fundingLock(ctx, tx, cmd.UserID)
	if err != nil {
		return nil, err
	}
	var prior, classified, legacyRefund decimal.Decimal
	var hasExisting bool
	err = tx.QueryRowContext(ctx, `SELECT
 coalesce((SELECT sum(c.amount-coalesce(x.cash_amount,0)-coalesce(x.bonus_amount,0)) FROM marketplace_payment_chargebacks c LEFT JOIN marketplace_chargeback_reclass_attributions x ON x.event_id=c.event_id WHERE c.order_id=$1),0)
 +coalesce((SELECT sum(a.amount-coalesce(x.cash_amount,0)-coalesce(x.bonus_amount,0)) FROM marketplace_payment_refund_attempts a LEFT JOIN marketplace_refund_reclass_attributions x ON x.attempt_id=a.attempt_id WHERE a.order_id=$1 AND a.state IN ('reserved','confirmed')),0),
 coalesce((SELECT sum(amount) FROM marketplace_funding_admin_operations WHERE kind='reclassify' AND order_id=$1),0),
 EXISTS(SELECT 1 FROM wallet_funding_lots WHERE (origin_type='payment_order' OR origin_type='payment_bonus') AND origin_id=$1::text),
 greatest(coalesce((SELECT CASE WHEN p.status IN ('PARTIALLY_REFUNDED','REFUNDED') AND NOT EXISTS(SELECT 1 FROM marketplace_payment_refund_attempts a WHERE a.order_id=p.id AND a.state='confirmed') THEN p.refund_amount ELSE 0 END FROM payment_orders p WHERE p.id=$1),0), coalesce((SELECT max(legacy_refund_amount) FROM marketplace_funding_admin_operations WHERE kind='reclassify' AND order_id=$1),0))`, cmd.OrderID).Scan(&prior, &classified, &hasExisting, &legacyRefund)
	if err != nil {
		return nil, err
	}
	prior = prior.Add(legacyRefund)
	if prior.IsNegative() {
		return nil, ErrFundingInvariant
	}
	if hasExisting && classified.IsZero() {
		return nil, service.ErrInvalidFunding
	}
	if prior.Add(classified).Add(cmd.Amount).GreaterThan(amount) {
		return nil, service.ErrInsufficientFunding
	}
	remaining := cmd.Amount
	for _, lot := range lots {
		if lot.Kind != service.FundingLegacy {
			continue
		}
		part := decimal.Min(remaining, lot.Remaining.Sub(lot.Reserved))
		if part.IsPositive() {
			if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_lots SET remaining=remaining-$2 WHERE id=$1`, lot.ID, part); err != nil {
				return nil, err
			}
			remaining = remaining.Sub(part)
		}
		if remaining.IsZero() {
			break
		}
	}
	if !remaining.IsZero() {
		return nil, service.ErrInsufficientFunding
	}
	result.CashAmount, result.BonusAmount = refundParts(cmd.Amount, prior.Add(classified), amount, bonus)
	// Origin identity remains the actual order, so later refund/chargeback
	// handling sees the same provenance. A zero cash lot is a valid identity.
	for _, part := range []struct {
		kind, origin string
		value        decimal.Decimal
	}{{"cash_paid", "payment_order", result.CashAmount}, {"promotional", "payment_bonus", result.BonusAmount}} {
		if part.value.IsZero() && part.origin == "payment_bonus" {
			continue
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT(origin_type,origin_id) DO UPDATE SET original=wallet_funding_lots.original+EXCLUDED.original,remaining=wallet_funding_lots.remaining+EXCLUDED.remaining WHERE wallet_funding_lots.user_id=EXCLUDED.user_id AND wallet_funding_lots.kind=EXCLUDED.kind`, cmd.UserID, part.kind, part.origin, fmt.Sprint(cmd.OrderID), part.value)
		if e != nil {
			return nil, e
		}
		n, e := res.RowsAffected()
		if e != nil || n != 1 {
			return nil, service.ErrInvalidFunding
		}
	}
	entries := []LedgerPosting{{Account: "funding_legacy", SubjectID: &cmd.UserID, Side: "debit", Amount: cmd.Amount}}
	if result.CashAmount.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "funding_cash", SubjectID: &cmd.UserID, Side: "credit", Amount: result.CashAmount})
	}
	if result.BonusAmount.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "funding_promotional", SubjectID: &cmd.UserID, Side: "credit", Amount: result.BonusAmount})
	}
	if err = NewMarketplaceLedgerRepository().Post(ctx, tx, "funding:reclass:"+cmd.OperationID, fmt.Sprintf("admin:%d", cmd.ActorID), "USD", entries); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO marketplace_funding_admin_operations(operation_id,kind,fingerprint,actor_id,user_id,order_id,amount,cash_amount,bonus_amount,evidence,verified_redeem_id,verified_redeem_code,verified_completed_at,legacy_refund_amount,verified_paid_at,verified_redeemed_at,remaining_provenance_confirmed) VALUES($1,'reclassify',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, cmd.OperationID, fp, cmd.ActorID, cmd.UserID, cmd.OrderID, cmd.Amount, result.CashAmount, result.BonusAmount, cmd.Evidence, redeemID, redeemCode, completed.Time, legacyRefund, paid.Time, redeemedAt, cmd.RemainingProvenanceConfirmed)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (r *userRepository) ListFundingRisks(ctx context.Context, actor int64) ([]service.FundingRisk, error) {
	db, err := r.fundingAdminDB()
	if err != nil {
		return nil, err
	}
	var admin bool
	if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin' AND status='active' AND deleted_at IS NULL)`, actor).Scan(&admin); err != nil {
		return nil, err
	}
	if !admin {
		return nil, service.ErrInvalidFunding
	}
	rows, err := db.QueryContext(ctx, `SELECT f.user_id,f.reason,f.version,f.created_at,s.debt FROM marketplace_payment_risk_flags f JOIN wallet_funding_states s ON s.user_id=f.user_id ORDER BY f.created_at,f.user_id LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.FundingRisk{}
	for rows.Next() {
		var x service.FundingRisk
		if err = rows.Scan(&x.UserID, &x.Reason, &x.Version, &x.CreatedAt, &x.Debt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (r *userRepository) ClearFundingRisk(ctx context.Context, cmd service.FundingRiskClearCommand) (*service.FundingRiskClearResult, error) {
	if !validFundingAdmin(cmd.ActorID, cmd.UserID, cmd.OperationID, cmd.Evidence) || cmd.ExpectedVersion <= 0 {
		return nil, service.ErrInvalidFunding
	}
	db, err := r.fundingAdminDB()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Both finance operations acquire the operation key before user locks.
	fp := payoutFingerprint([]any{"clear_risk", cmd.ActorID, cmd.UserID, cmd.ExpectedVersion, cmd.Evidence})
	replay, opErr := lockFundingAdminOperation(ctx, tx, cmd.OperationID, "clear_risk", fp)
	if opErr != nil && !errors.Is(opErr, service.ErrFundingAdminConflict) {
		return nil, opErr
	}
	if err = lockFundingAdminUsers(ctx, tx, cmd.ActorID, cmd.UserID); err != nil {
		return nil, err
	}
	if opErr != nil {
		return nil, opErr
	}
	result := &service.FundingRiskClearResult{UserID: cmd.UserID, Version: cmd.ExpectedVersion, OperationID: cmd.OperationID, Replay: replay}
	if replay {
		return result, tx.Commit()
	}
	_, _, debt, _, err := fundingLock(ctx, tx, cmd.UserID)
	if err != nil {
		return nil, err
	}
	if debt.IsPositive() {
		return nil, service.ErrInvalidFunding
	}
	var version int64
	var priorReason string
	err = tx.QueryRowContext(ctx, `SELECT version,reason FROM marketplace_payment_risk_flags WHERE user_id=$1 FOR UPDATE`, cmd.UserID).Scan(&version, &priorReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrFundingRiskStale
	}
	if err != nil {
		return nil, err
	}
	if version != cmd.ExpectedVersion {
		return nil, service.ErrFundingRiskStale
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO marketplace_funding_admin_operations(operation_id,kind,fingerprint,actor_id,user_id,risk_version,evidence,prior_reason) VALUES($1,'clear_risk',$2,$3,$4,$5,$6,$7)`, cmd.OperationID, fp, cmd.ActorID, cmd.UserID, version, cmd.Evidence, priorReason)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM marketplace_payment_risk_flags WHERE user_id=$1 AND version=$2`, cmd.UserID, version); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}
