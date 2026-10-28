package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type marketplaceRequestRepository struct{ db *sql.DB }

func NewMarketplaceRequestRepository(db *sql.DB) service.MarketplaceRequestRepository {
	return &marketplaceRequestRepository{db: db}
}

const receiptColumns = `id::text,buyer_user_id,api_key_id,group_id,request_digest,idempotency_key,status,budget::text,actual_charge::text,billable_attempt_id`

type marketplaceRowScanner interface{ Scan(...any) error }

func scanMarketplaceReceipt(row marketplaceRowScanner) (*service.MarketplaceReceipt, error) {
	var r service.MarketplaceReceipt
	var budget, charge string
	if err := row.Scan(&r.ID, &r.BuyerUserID, &r.APIKeyID, &r.GroupID, &r.RequestDigest, &r.IdempotencyKey, &r.Status, &budget, &charge, &r.BillableAttemptID); err != nil {
		return nil, err
	}
	b, err := decimal.NewFromString(budget)
	if err != nil {
		return nil, err
	}
	c, err := decimal.NewFromString(charge)
	if err != nil {
		return nil, err
	}
	r.Budget = service.Money{Decimal: b}
	r.ActualCharge = service.Money{Decimal: c}
	return &r, nil
}
func (r *marketplaceRequestRepository) Prepare(ctx context.Context, c service.MarketplacePrepareCommand) (*service.MarketplaceReceipt, bool, error) {
	if c.BuyerUserID <= 0 || c.APIKeyID <= 0 || c.GroupID <= 0 || strings.TrimSpace(c.RequestDigest) == "" || (c.IdempotencyKey != nil && (strings.TrimSpace(*c.IdempotencyKey) == "" || len(*c.IdempotencyKey) > 255)) {
		return nil, false, service.ErrMarketplaceRequestInvalid
	}
	var ownsKey bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys WHERE id=$1 AND user_id=$2)`, c.APIKeyID, c.BuyerUserID).Scan(&ownsKey); err != nil {
		return nil, false, err
	}
	if !ownsKey {
		return nil, false, service.ErrMarketplaceRequestInvalid
	}
	id := uuid.NewString()
	row := r.db.QueryRowContext(ctx, `INSERT INTO marketplace_requests (id,buyer_user_id,api_key_id,group_id,request_digest,idempotency_key) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (buyer_user_id,api_key_id,idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING RETURNING `+receiptColumns, id, c.BuyerUserID, c.APIKeyID, c.GroupID, c.RequestDigest, c.IdempotencyKey)
	receipt, err := scanMarketplaceReceipt(row)
	if err == nil {
		return receipt, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	receipt, err = scanMarketplaceReceipt(r.db.QueryRowContext(ctx, `SELECT `+receiptColumns+` FROM marketplace_requests WHERE buyer_user_id=$1 AND api_key_id=$2 AND idempotency_key=$3`, c.BuyerUserID, c.APIKeyID, c.IdempotencyKey))
	if err != nil {
		return nil, false, err
	}
	if receipt.RequestDigest != c.RequestDigest || receipt.GroupID != c.GroupID {
		return nil, false, service.ErrMarketplaceIdempotencyConflict
	}
	return receipt, false, nil
}
func (r *marketplaceRequestRepository) GetOwned(ctx context.Context, id string, buyer, key int64) (*service.MarketplaceReceipt, error) {
	return scanMarketplaceReceipt(r.db.QueryRowContext(ctx, `SELECT `+receiptColumns+` FROM marketplace_requests WHERE id=$1 AND buyer_user_id=$2 AND api_key_id=$3`, id, buyer, key))
}
func lockMarketplaceReceipt(ctx context.Context, tx *sql.Tx, id string) (*service.MarketplaceReceipt, error) {
	if tx == nil {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	return scanMarketplaceReceipt(tx.QueryRowContext(ctx, `SELECT `+receiptColumns+` FROM marketplace_requests WHERE id=$1 FOR UPDATE`, id))
}
func validMoney(m service.Money) bool {
	return !m.IsNegative() && m.Equal(m.Round(8)) && m.LessThan(decimal.NewFromInt(1000000000000))
}
func (r *marketplaceRequestRepository) Reserve(ctx context.Context, tx *sql.Tx, id string, budget service.Money) error {
	if !validMoney(budget) {
		return service.ErrMarketplaceRequestInvalid
	}
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	if receipt.Status != "prepared" && receipt.Status != "budget_reserved" && receipt.Status != "needs_review" {
		return service.ErrMarketplaceRequestState
	}
	if budget.LessThan(receipt.Budget.Decimal) {
		return service.ErrMarketplaceRequestInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status=CASE WHEN status='needs_review' THEN status ELSE 'budget_reserved' END,budget=$2,updated_at=now() WHERE id=$1`, id, budget.String())
	return err
}
func (r *marketplaceRequestRepository) CreateAttempt(ctx context.Context, tx *sql.Tx, id string, q service.QuoteSnapshot) (*service.MarketplaceAttempt, error) {
	digest, err := service.MarketplaceQuoteDigest(q)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if receipt.Status != "budget_reserved" || receipt.BillableAttemptID != nil {
		return nil, service.ErrMarketplaceRequestState
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_attempts WHERE receipt_id=$1 AND status IN ('prepared','possibly_executed','usage_recorded','settled'))`, id).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, service.ErrMarketplaceRequestState
	}
	a := &service.MarketplaceAttempt{ReceiptID: id, AccountID: q.AccountID, OfferID: q.OfferID, OwnerUserID: q.OwnerUserID, Quote: q, Status: "prepared"}
	err = tx.QueryRowContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status) VALUES($1,(SELECT COALESCE(MAX(ordinal),0)+1 FROM marketplace_attempts WHERE receipt_id=$1),$2,$3,$4,$5,$6,'prepared') RETURNING id,ordinal`, id, q.AccountID, q.OfferID, q.OwnerUserID, string(data), digest).Scan(&a.ID, &a.Ordinal)
	if err != nil {
		return nil, err
	}
	return a, nil
}
func lockMarketplaceAttempt(ctx context.Context, tx *sql.Tx, id string, attempt int64) (string, []byte, []byte, error) {
	var status string
	var quote, usage []byte
	err := tx.QueryRowContext(ctx, `SELECT status,quote,usage FROM marketplace_attempts WHERE receipt_id=$1 AND id=$2 FOR UPDATE`, id, attempt).Scan(&status, &quote, &usage)
	return status, quote, usage, err
}
func (r *marketplaceRequestRepository) MarkDispatched(ctx context.Context, tx *sql.Tx, id string, attempt int64) error {
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	status, _, _, err := lockMarketplaceAttempt(ctx, tx, id, attempt)
	if err != nil {
		return err
	}
	if receipt.Status != "budget_reserved" || receipt.BillableAttemptID != nil || status != "prepared" {
		return service.ErrMarketplaceRequestState
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status='possibly_executed',updated_at=now() WHERE id=$1`, attempt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='dispatched',billable_attempt_id=$2,updated_at=now() WHERE id=$1`, id, attempt)
	return err
}
func (r *marketplaceRequestRepository) ClassifyFailure(ctx context.Context, tx *sql.Tx, id string, attempt int64, class string) error {
	if class != service.MarketplaceDefinitelyNotDispatched && class != service.MarketplaceRejectedBeforeExecution && class != service.MarketplacePossiblyExecuted {
		class = service.MarketplacePossiblyExecuted
	}
	if class == service.MarketplacePossiblyExecuted {
		return r.MarkUncertain(ctx, tx, id, attempt, class)
	}
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	status, _, _, err := lockMarketplaceAttempt(ctx, tx, id, attempt)
	if err != nil {
		return err
	}
	if status != "prepared" && status != "possibly_executed" {
		return service.ErrMarketplaceRequestState
	}
	if receipt.Status != "budget_reserved" && receipt.Status != "dispatched" {
		return service.ErrMarketplaceRequestState
	}
	if receipt.BillableAttemptID != nil && *receipt.BillableAttemptID != attempt {
		return service.ErrMarketplaceRequestState
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status=$2,updated_at=now() WHERE id=$1`, attempt, class); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='budget_reserved',billable_attempt_id=NULL,updated_at=now() WHERE id=$1`, id)
	return err
}
func (r *marketplaceRequestRepository) MarkUncertain(ctx context.Context, tx *sql.Tx, id string, attempt int64, reason string) error {
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	status, _, _, err := lockMarketplaceAttempt(ctx, tx, id, attempt)
	if err != nil {
		return err
	}
	if (receipt.Status != "budget_reserved" && receipt.Status != "dispatched") || (status != "prepared" && status != "possibly_executed") || (receipt.BillableAttemptID != nil && *receipt.BillableAttemptID != attempt) {
		return service.ErrMarketplaceRequestState
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status='possibly_executed',updated_at=now() WHERE id=$1`, attempt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='usage_pending',billable_attempt_id=$2,review_reason=$3,updated_at=now() WHERE id=$1`, id, attempt, reason)
	return err
}
func (r *marketplaceRequestRepository) RecordUsage(ctx context.Context, tx *sql.Tx, id string, attempt int64, usage service.UsageSnapshot) error {
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	status, quote, _, err := lockMarketplaceAttempt(ctx, tx, id, attempt)
	if err != nil {
		return err
	}
	if (receipt.Status != "dispatched" && receipt.Status != "usage_pending" && receipt.Status != "needs_review") || receipt.BillableAttemptID == nil || *receipt.BillableAttemptID != attempt || status != "possibly_executed" {
		return service.ErrMarketplaceRequestState
	}
	var q service.QuoteSnapshot
	if err = json.Unmarshal(quote, &q); err != nil {
		return err
	}
	if _, err = service.NewMarketplaceQuoteService().CalculateActual(q, usage); err != nil {
		return err
	}
	data, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status='usage_recorded',usage=$2,updated_at=now() WHERE id=$1`, attempt, string(data)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status=CASE WHEN status='needs_review' THEN status ELSE 'dispatched' END,review_reason=CASE WHEN status='needs_review' THEN review_reason ELSE NULL END,updated_at=now() WHERE id=$1`, id)
	return err
}
func (r *marketplaceRequestRepository) MarkSettled(ctx context.Context, tx *sql.Tx, id string, attempt int64, charge service.Money) error {
	if !validMoney(charge) {
		return service.ErrMarketplaceRequestInvalid
	}
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	status, quote, data, err := lockMarketplaceAttempt(ctx, tx, id, attempt)
	if err != nil {
		return err
	}
	if (receipt.Status != "dispatched" && receipt.Status != "needs_review") || receipt.BillableAttemptID == nil || *receipt.BillableAttemptID != attempt || status != "usage_recorded" {
		return service.ErrMarketplaceRequestState
	}
	var q service.QuoteSnapshot
	var usage service.UsageSnapshot
	if err = json.Unmarshal(quote, &q); err != nil {
		return err
	}
	if err = json.Unmarshal(data, &usage); err != nil {
		return err
	}
	calculated, err := service.NewMarketplaceQuoteService().CalculateActual(q, usage)
	if err != nil {
		return err
	}
	if !calculated.Equal(charge.Decimal) || charge.GreaterThan(receipt.Budget.Decimal) {
		return fmt.Errorf("%w: charge or budget mismatch", service.ErrMarketplaceRequestInvalid)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status='settled',updated_at=now() WHERE id=$1`, attempt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='settled',actual_charge=$2,updated_at=now() WHERE id=$1`, id, charge.String())
	return err
}

// MarkNeedsReview preserves the hold and recorded usage while settlement is
// impossible, e.g. when the actual charge exceeds available eligible funding.
func (r *marketplaceRequestRepository) MarkNeedsReview(ctx context.Context, tx *sql.Tx, id string, attempt int64, reason string) error {
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	status, _, _, err := lockMarketplaceAttempt(ctx, tx, id, attempt)
	if err != nil {
		return err
	}
	if (receipt.Status != "dispatched" && receipt.Status != "usage_pending") || receipt.BillableAttemptID == nil || *receipt.BillableAttemptID != attempt || (status != "usage_recorded" && status != "possibly_executed") || strings.TrimSpace(reason) == "" {
		return service.ErrMarketplaceRequestState
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='needs_review',review_reason=$2,updated_at=now() WHERE id=$1`, id, reason)
	return err
}

// MarkVoided must share the transaction that releases any real funding hold.
func (r *marketplaceRequestRepository) MarkVoided(ctx context.Context, tx *sql.Tx, id string) error {
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	if (receipt.Status != "prepared" && receipt.Status != "budget_reserved") || receipt.BillableAttemptID != nil {
		return service.ErrMarketplaceRequestState
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_attempts WHERE receipt_id=$1 AND status IN ('prepared','possibly_executed','usage_recorded','settled'))`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return service.ErrMarketplaceRequestState
	}
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='voided',updated_at=now() WHERE id=$1`, id)
	return err
}
