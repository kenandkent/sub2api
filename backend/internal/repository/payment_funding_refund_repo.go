package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

// refundExecutor requires the same Ent transaction used for the payment state.
func (r *userRepository) refundExecutor(ctx context.Context) (sqlExecutor, error) {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return nil, errors.New("payment funding refund requires transaction")
	}
	exec := sqlExecutorFromEntClient(tx.Client())
	if exec == nil {
		return nil, errors.New("payment funding refund transaction has no SQL executor")
	}
	return exec, nil
}

func refundUserUpdate(ctx context.Context, q sqlExecutor, query string, args ...any) error {
	var previous sql.NullString
	if err := scanSingleRow(ctx, q, `SELECT current_setting('sub2api.funding_explicit', true)`, nil, &previous); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `SELECT set_config('sub2api.funding_explicit','true',true)`); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `SELECT set_config('sub2api.funding_explicit',$1,true)`, previous.String)
	return err
}

func refundPost(ctx context.Context, q sqlExecutor, op string, user int64, debit, credit string, amount decimal.Decimal) error {
	var id int64
	if err := scanSingleRow(ctx, q, `INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint) VALUES($1,'payment_refund','USD',$2) RETURNING id`, []any{op, op}, &id); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO ledger_entries(transaction_id,account,subject_id,side,amount) VALUES($1,$2,$4,'debit',$5),($1,$3,$4,'credit',$5)`, id, debit, credit, user, amount); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE ledger_transactions SET sealed=true WHERE id=$1`, id)
	return err
}

// ReservePaymentRefund returns false only for users not yet migrated. Migrated
// users can reserve exclusively the unspent, unheld cash belonging to this order.
func (r *userRepository) ReservePaymentRefund(ctx context.Context, order, user int64, amount float64, attempt string) (bool, error) {
	q, err := r.refundExecutor(ctx)
	if err != nil {
		return false, err
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return false, service.ErrInvalidFunding
	}
	a, err := fundingAmount(decimal.NewFromFloat(amount))
	if err != nil || attempt == "" {
		return false, service.ErrInvalidFunding
	}
	var balance, frozen, debt decimal.Decimal
	if err = scanSingleRow(ctx, q, `SELECT balance,frozen_balance FROM users WHERE id=$1 FOR UPDATE`, []any{user}, &balance, &frozen); err != nil {
		return false, err
	}
	var version int
	err = scanSingleRow(ctx, q, `SELECT debt,migration_version FROM wallet_funding_states WHERE user_id=$1 FOR UPDATE`, []any{user}, &debt, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if version != 1 {
		return true, ErrFundingInvariant
	}
	var existingOrder, existingUser int64
	var existingAmount decimal.Decimal
	var state string
	err = scanSingleRow(ctx, q, `SELECT order_id,user_id,amount,state FROM marketplace_payment_refund_attempts WHERE attempt_id=$1`, []any{attempt}, &existingOrder, &existingUser, &existingAmount, &state)
	if err == nil {
		if existingOrder != order || existingUser != user || !existingAmount.Equal(a) || state != "reserved" {
			return true, ErrFundingOperationConflict
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return true, err
	}
	// Lock all lots in fixed order, and verify the projection before touching it.
	rows, err := q.QueryContext(ctx, `SELECT id,kind,origin_type,origin_id,remaining,reserved FROM wallet_funding_lots WHERE user_id=$1 ORDER BY id FOR UPDATE`, user)
	if err != nil {
		return true, err
	}
	total, held := decimal.Zero, decimal.Zero
	var lot int64
	available := decimal.Zero
	for rows.Next() {
		var id int64
		var kind, origin, originID string
		var remaining, reserved decimal.Decimal
		if err = rows.Scan(&id, &kind, &origin, &originID, &remaining, &reserved); err != nil {
			_ = rows.Close()
			return true, err
		}
		total = total.Add(remaining)
		held = held.Add(reserved)
		if kind == "cash_paid" && origin == "payment_order" && originID == strconv.FormatInt(order, 10) {
			lot = id
			available = remaining.Sub(reserved)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return true, err
	}
	if !total.Sub(debt).Equal(balance.Add(frozen)) || !held.Equal(frozen) {
		return true, ErrFundingInvariant
	}
	if lot == 0 || debt.IsPositive() || balance.LessThan(a) || available.LessThan(a) {
		return true, service.ErrInsufficientFunding
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO marketplace_payment_refund_attempts(attempt_id,order_id,user_id,lot_id,amount,state) VALUES($1,$2,$3,$4,$5,'reserved')`, attempt, order, user, lot, a); err != nil {
		return true, err
	}
	holdID := "payment-refund:" + attempt
	if _, err = q.ExecContext(ctx, `INSERT INTO wallet_funding_operations(operation_id,user_id,actor,operation_type,fingerprint) VALUES($1,$2,'payment_refund','refund_hold',$3)`, holdID, user, a.StringFixed(8)); err != nil {
		return true, err
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO wallet_funding_allocations(lot_id,hold_operation_id,reserved) VALUES($1,$2,$3)`, lot, holdID, a); err != nil {
		return true, err
	}
	if _, err = q.ExecContext(ctx, `UPDATE wallet_funding_lots SET reserved=reserved+$2 WHERE id=$1`, lot, a); err != nil {
		return true, err
	}
	if err = refundUserUpdate(ctx, q, `UPDATE users SET balance=balance-$2,frozen_balance=frozen_balance+$2 WHERE id=$1`, user, a); err != nil {
		return true, err
	}
	return true, refundPost(ctx, q, "payment-refund-reserve:"+attempt, user, "buyer_available", "buyer_frozen", a)
}

// SettlePaymentRefund is idempotent and retains the originating lot. Releases
// first retire debt accumulated while the refund was pending.
func (r *userRepository) SettlePaymentRefund(ctx context.Context, attempt string, confirm bool) error {
	q, err := r.refundExecutor(ctx)
	if err != nil {
		return err
	}
	var user, lot int64
	var amount decimal.Decimal
	var state string
	// Read identity before taking the prescribed user -> state -> lot locks.
	if err = scanSingleRow(ctx, q, `SELECT user_id,lot_id,amount,state FROM marketplace_payment_refund_attempts WHERE attempt_id=$1`, []any{attempt}, &user, &lot, &amount, &state); err != nil {
		return err
	}
	var balance, frozen, debt decimal.Decimal
	if err = scanSingleRow(ctx, q, `SELECT balance,frozen_balance FROM users WHERE id=$1 FOR UPDATE`, []any{user}, &balance, &frozen); err != nil {
		return err
	}
	if err = scanSingleRow(ctx, q, `SELECT debt FROM wallet_funding_states WHERE user_id=$1 FOR UPDATE`, []any{user}, &debt); err != nil {
		return err
	}
	var remaining, reserved decimal.Decimal
	if err = scanSingleRow(ctx, q, `SELECT remaining,reserved FROM wallet_funding_lots WHERE id=$1 AND user_id=$2 FOR UPDATE`, []any{lot, user}, &remaining, &reserved); err != nil {
		return err
	}
	if err = scanSingleRow(ctx, q, `SELECT state FROM marketplace_payment_refund_attempts WHERE attempt_id=$1 FOR UPDATE`, []any{attempt}, &state); err != nil {
		return err
	}
	target := "released"
	if confirm {
		target = "confirmed"
	}
	if state == target {
		return nil
	}
	if state != "reserved" {
		return ErrFundingOperationConflict
	}
	if reserved.LessThan(amount) || remaining.LessThan(amount) || frozen.LessThan(amount) {
		return ErrFundingInvariant
	}
	paid := decimal.Zero
	if !confirm {
		paid = decimal.Min(amount, debt)
	}
	remove := paid
	if confirm {
		remove = amount
	}
	if _, err = q.ExecContext(ctx, `UPDATE wallet_funding_lots SET remaining=remaining-$2,reserved=reserved-$3 WHERE id=$1`, lot, remove, amount); err != nil {
		return err
	}
	if paid.IsPositive() {
		if _, err = q.ExecContext(ctx, `UPDATE wallet_funding_states SET debt=debt-$2 WHERE user_id=$1`, user, paid); err != nil {
			return err
		}
	}
	if confirm {
		err = refundUserUpdate(ctx, q, `UPDATE users SET frozen_balance=frozen_balance-$2 WHERE id=$1`, user, amount)
	} else {
		err = refundUserUpdate(ctx, q, `UPDATE users SET balance=balance+$2,frozen_balance=frozen_balance-$2 WHERE id=$1`, user, amount)
	}
	if err != nil {
		return err
	}
	var allocationResult sql.Result
	if confirm {
		allocationResult, err = q.ExecContext(ctx, `UPDATE wallet_funding_allocations SET captured=captured+$2 WHERE hold_operation_id=$1 AND lot_id=$3 AND captured+released=0`, "payment-refund:"+attempt, amount, lot)
	} else {
		allocationResult, err = q.ExecContext(ctx, `UPDATE wallet_funding_allocations SET released=released+$2 WHERE hold_operation_id=$1 AND lot_id=$3 AND captured+released=0`, "payment-refund:"+attempt, amount, lot)
	}
	if err != nil {
		return err
	}
	changed, err := allocationResult.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrFundingInvariant
	}
	if _, err = q.ExecContext(ctx, `UPDATE marketplace_payment_refund_attempts SET state=$2,needs_review=false,updated_at=now() WHERE attempt_id=$1`, attempt, target); err != nil {
		return err
	}
	credit := "buyer_available"
	if confirm {
		credit = "payment_refund_outflow"
	}
	if err = refundPost(ctx, q, "payment-refund-"+target+":"+attempt, user, "buyer_frozen", credit, amount); err != nil {
		return fmt.Errorf("refund ledger: %w", err)
	}
	return nil
}

// RecordPaymentChargeback handles an independently verified bank event. It
// never changes supplier earnings or consumes another origin's funding.
func (r *userRepository) RecordPaymentChargeback(ctx context.Context, order, user, actor int64, value float64, event, evidence string) error {
	event, evidence = strings.TrimSpace(event), strings.TrimSpace(evidence)
	q, err := r.refundExecutor(ctx)
	if err != nil {
		return err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return service.ErrInvalidFunding
	}
	amount, err := fundingAmount(decimal.NewFromFloat(value))
	if err != nil || event == "" || evidence == "" || actor <= 0 {
		return service.ErrInvalidFunding
	}
	var balance, frozen, debt decimal.Decimal
	if err = scanSingleRow(ctx, q, `SELECT balance,frozen_balance FROM users WHERE id=$1 FOR UPDATE`, []any{user}, &balance, &frozen); err != nil {
		return err
	}
	var version int
	if err = scanSingleRow(ctx, q, `SELECT debt,migration_version FROM wallet_funding_states WHERE user_id=$1 FOR UPDATE`, []any{user}, &debt, &version); err != nil {
		return err
	}
	if version != 1 {
		return ErrFundingInvariant
	}
	var oldOrder, oldUser, oldActor int64
	var oldAmount decimal.Decimal
	var oldEvidence string
	err = scanSingleRow(ctx, q, `SELECT order_id,user_id,actor_id,amount,evidence FROM marketplace_payment_chargebacks WHERE event_id=$1`, []any{event}, &oldOrder, &oldUser, &oldActor, &oldAmount, &oldEvidence)
	if err == nil {
		if oldOrder != order || oldUser != user || oldActor != actor || !oldAmount.Equal(amount) || oldEvidence != evidence {
			return ErrFundingOperationConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var orderAmount decimal.Decimal
	if err = scanSingleRow(ctx, q, `SELECT amount FROM payment_orders WHERE id=$1 AND user_id=$2 AND order_type='balance' AND paid_at IS NOT NULL`, []any{order, user}, &orderAmount); err != nil {
		return err
	}
	var prior decimal.Decimal
	var open bool
	if err = scanSingleRow(ctx, q, `SELECT coalesce((SELECT sum(amount) FROM marketplace_payment_chargebacks WHERE order_id=$1),0)+coalesce((SELECT sum(amount) FROM marketplace_payment_refund_attempts WHERE order_id=$1 AND state='confirmed'),0), EXISTS(SELECT 1 FROM marketplace_payment_refund_attempts WHERE order_id=$1 AND state='reserved')`, []any{order}, &prior, &open); err != nil {
		return err
	}
	if open || prior.Add(amount).GreaterThan(orderAmount) {
		return service.ErrInsufficientFunding
	}
	rows, err := q.QueryContext(ctx, `SELECT id,kind,origin_type,origin_id,remaining,reserved FROM wallet_funding_lots WHERE user_id=$1 ORDER BY id FOR UPDATE`, user)
	if err != nil {
		return err
	}
	total, held, available := decimal.Zero, decimal.Zero, decimal.Zero
	var lot int64
	for rows.Next() {
		var id int64
		var kind, origin, originID string
		var rem, res decimal.Decimal
		if err = rows.Scan(&id, &kind, &origin, &originID, &rem, &res); err != nil {
			_ = rows.Close()
			return err
		}
		total = total.Add(rem)
		held = held.Add(res)
		if kind == "cash_paid" && origin == "payment_order" && originID == strconv.FormatInt(order, 10) {
			lot = id
			available = rem.Sub(res)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if !total.Sub(debt).Equal(balance.Add(frozen)) || !held.Equal(frozen) {
		return ErrFundingInvariant
	}
	revoked := decimal.Min(amount, available)
	newDebt := amount.Sub(revoked)
	if revoked.IsPositive() {
		if _, err = q.ExecContext(ctx, `UPDATE wallet_funding_lots SET remaining=remaining-$2 WHERE id=$1`, lot, revoked); err != nil {
			return err
		}
	}
	if newDebt.IsPositive() {
		if _, err = q.ExecContext(ctx, `UPDATE wallet_funding_states SET debt=debt+$2 WHERE user_id=$1`, user, newDebt); err != nil {
			return err
		}
	}
	if err = refundUserUpdate(ctx, q, `UPDATE users SET balance=balance-$2 WHERE id=$1`, user, amount); err != nil {
		return err
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO marketplace_payment_chargebacks(event_id,order_id,user_id,actor_id,amount,revoked,buyer_debt,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, event, order, user, actor, amount, revoked, newDebt, evidence); err != nil {
		return err
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO marketplace_payment_risk_flags(user_id,reason) VALUES($1,$2) ON CONFLICT(user_id) DO NOTHING`, user, "external chargeback: "+event); err != nil {
		return err
	}
	return refundPost(ctx, q, "payment-chargeback:"+event, user, "chargeback_loss", "buyer_chargeback_liability", amount)
}

// BindRefundProviderReference ensures one external refund belongs to at most one attempt.
func (r *userRepository) BindRefundProviderReference(ctx context.Context, attempt, reference string) error {
	if reference == "" {
		return nil
	}
	q, err := r.refundExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := q.ExecContext(ctx, `UPDATE marketplace_payment_refund_attempts SET provider_refund_id=$2 WHERE attempt_id=$1 AND (provider_refund_id IS NULL OR provider_refund_id=$2)`, attempt, reference)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFundingOperationConflict
	}
	return nil
}

// MarkRefundNeedsReview retains the hold for an inconclusive provider query.
func (r *userRepository) MarkRefundNeedsReview(ctx context.Context, attempt string) error {
	q, err := r.refundExecutor(ctx)
	if err != nil {
		return err
	}
	result, err := q.ExecContext(ctx, `UPDATE marketplace_payment_refund_attempts SET needs_review=true,updated_at=now() WHERE attempt_id=$1 AND state='reserved'`, attempt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFundingOperationConflict
	}
	return nil
}

// ResolveUncertainPaymentFunding atomically settles a reviewed attempt and
// persists the immutable operator decision. Caller holds the payment order lock.
func (r *userRepository) ResolveUncertainPaymentFunding(ctx context.Context, cmd service.UncertainFundingResolution, expectedAmount float64, allowNew bool) (int64, bool, error) {
	q, err := r.refundExecutor(ctx)
	if err != nil {
		return 0, false, err
	}
	var prev service.UncertainFundingResolution
	var prevUser int64
	var priorAmount decimal.Decimal
	var ref sql.NullString
	err = scanSingleRow(ctx, q, `SELECT order_id,user_id,attempt_id,actor_id,decision,provider_refund_id,evidence,amount FROM marketplace_payment_refund_resolutions WHERE operation_id=$1`, []any{cmd.OperationID}, &prev.OrderID, &prevUser, &prev.AttemptID, &prev.ActorID, &prev.Decision, &ref, &prev.Evidence, &priorAmount)
	if err == nil {
		if prev.OrderID != cmd.OrderID || prev.AttemptID != cmd.AttemptID || prev.ActorID != cmd.ActorID || prev.Decision != cmd.Decision || ref.String != cmd.ProviderRefundID || prev.Evidence != cmd.Evidence || !priorAmount.Equal(service.QuantizeFunding(decimal.NewFromFloat(expectedAmount))) {
			return 0, false, ErrFundingOperationConflict
		}
		return prevUser, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	if !allowNew {
		return 0, false, ErrFundingOperationConflict
	}
	var order, user int64
	var amount decimal.Decimal
	var state string
	var review bool
	if err = scanSingleRow(ctx, q, `SELECT order_id,user_id,amount,state,needs_review FROM marketplace_payment_refund_attempts WHERE attempt_id=$1`, []any{cmd.AttemptID}, &order, &user, &amount, &state, &review); err != nil {
		return 0, false, err
	}
	if order != cmd.OrderID || state != "reserved" || !review || !amount.Equal(service.QuantizeFunding(decimal.NewFromFloat(expectedAmount))) {
		return 0, false, ErrFundingOperationConflict
	}
	if cmd.Decision == "confirmed_refunded" {
		if err = r.BindRefundProviderReference(ctx, cmd.AttemptID, cmd.ProviderRefundID); err != nil {
			return 0, false, err
		}
	}
	if err = r.SettlePaymentRefund(ctx, cmd.AttemptID, cmd.Decision == "confirmed_refunded"); err != nil {
		return 0, false, err
	}
	var providerID any
	if cmd.ProviderRefundID != "" {
		providerID = cmd.ProviderRefundID
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO marketplace_payment_refund_resolutions(operation_id,attempt_id,order_id,user_id,actor_id,decision,provider_refund_id,evidence,amount) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, cmd.OperationID, cmd.AttemptID, cmd.OrderID, user, cmd.ActorID, cmd.Decision, providerID, cmd.Evidence, amount); err != nil {
		return 0, false, err
	}
	return user, false, nil
}

func (r *userRepository) ListPendingFundingRefunds(ctx context.Context, limit int) ([]service.FundingRefundReviewItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	q := txAwareSQLExecutor(ctx, r.sql, r.client)
	if q == nil {
		return nil, errors.New("payment funding refund SQL executor unavailable")
	}
	rows, err := q.QueryContext(ctx, `SELECT a.attempt_id,a.order_id,a.user_id,a.amount,a.created_at,a.updated_at FROM marketplace_payment_refund_attempts a WHERE a.state='reserved' AND a.needs_review ORDER BY a.updated_at,a.attempt_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []service.FundingRefundReviewItem{}
	for rows.Next() {
		var item service.FundingRefundReviewItem
		var amount decimal.Decimal
		if err = rows.Scan(&item.AttemptID, &item.OrderID, &item.UserID, &amount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Amount, _ = amount.Float64()
		result = append(result, item)
	}
	return result, rows.Err()
}
