package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type MarketplaceGatewayRepository struct {
	db  *sql.DB
	cfg *config.Config
}

func NewMarketplaceGatewayRepository(db *sql.DB, cfg *config.Config) *MarketplaceGatewayRepository {
	return &MarketplaceGatewayRepository{db: db, cfg: cfg}
}

// lockBuyer serializes with key, group and user changes and checks the current
// funding projection rather than migrating or crediting unverified funds.
func lockMarketplaceBuyer(ctx context.Context, tx *sql.Tx, buyer, key, group int64) error {
	if _, _, _, _, err := fundingLock(ctx, tx, buyer); err != nil {
		return err
	}
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT u.status='active' AND u.deleted_at IS NULL AND k.status='active' AND k.deleted_at IS NULL AND k.user_id=u.id AND k.group_id=g.id AND g.status='active' AND g.deleted_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND (k.quota<=0 OR k.quota_used<k.quota) AND (k.rate_limit_5h<=0 OR k.window_5h_start IS NULL OR k.window_5h_start<=now()-interval '5 hours' OR k.usage_5h<k.rate_limit_5h) AND (k.rate_limit_1d<=0 OR k.window_1d_start IS NULL OR k.window_1d_start<=now()-interval '1 day' OR k.usage_1d<k.rate_limit_1d) AND (k.rate_limit_7d<=0 OR k.window_7d_start IS NULL OR k.window_7d_start<=now()-interval '7 days' OR k.usage_7d<k.rate_limit_7d) AND f.migration_version>0 AND f.reconciled_at IS NOT NULL AND f.debt=0 AND NOT EXISTS(SELECT 1 FROM marketplace_payment_risk_flags WHERE user_id=u.id) FROM users u JOIN api_keys k ON k.id=$2 JOIN groups g ON g.id=$3 JOIN wallet_funding_states f ON f.user_id=u.id WHERE u.id=$1 FOR SHARE OF u,k,g,f`, buyer, key, group).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrMarketplaceRequestInvalid
	}
	if err != nil {
		return err
	}
	if !ok {
		return service.ErrMarketplaceRequestInvalid
	}
	return nil
}

type marketplaceReplayQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// lookupMarketplaceReplay reveals a receipt only to its authenticated owner and
// exact API key. Quota, debt and feature state control new requests, not reads.
func lookupMarketplaceReplay(ctx context.Context, q marketplaceReplayQuery, c service.MarketplacePrepareCommand) (*service.MarketplaceReceipt, error) {
	if c.IdempotencyKey == nil {
		return nil, nil
	}
	if c.BuyerUserID <= 0 || c.APIKeyID <= 0 || c.GroupID <= 0 || strings.TrimSpace(c.RequestDigest) == "" || strings.TrimSpace(*c.IdempotencyKey) == "" || len(*c.IdempotencyKey) > 255 {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	receipt, err := scanMarketplaceReceipt(q.QueryRowContext(ctx, `SELECT `+receiptColumns+` FROM marketplace_requests r WHERE r.buyer_user_id=$1 AND r.api_key_id=$2 AND r.idempotency_key=$3 AND EXISTS (SELECT 1 FROM users u JOIN api_keys k ON k.user_id=u.id WHERE u.id=$1 AND u.status='active' AND u.deleted_at IS NULL AND k.id=$2 AND k.deleted_at IS NULL AND k.status IN ('active','quota_exhausted') AND (k.expires_at IS NULL OR k.expires_at>now()))`, c.BuyerUserID, c.APIKeyID, *c.IdempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if receipt.GroupID != c.GroupID || receipt.RequestDigest != c.RequestDigest {
		return nil, service.ErrMarketplaceIdempotencyConflict
	}
	return receipt, nil
}

// MarketplaceReplayExists is an admission hint only. A positive result must
// lead to LookupReplay with the actual payload digest, and never to dispatch.
func (r *MarketplaceGatewayRepository) MarketplaceReplayExists(ctx context.Context, buyer, key, group int64, idempotency string) (bool, error) {
	if buyer <= 0 || key <= 0 || group <= 0 || strings.TrimSpace(idempotency) == "" || len(idempotency) > 255 {
		return false, nil
	}
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_requests r JOIN users u ON u.id=r.buyer_user_id JOIN api_keys k ON k.id=r.api_key_id AND k.user_id=u.id WHERE r.buyer_user_id=$1 AND r.api_key_id=$2 AND r.group_id=$3 AND r.idempotency_key=$4 AND u.status='active' AND u.deleted_at IS NULL AND k.deleted_at IS NULL AND k.status IN ('active','quota_exhausted') AND (k.expires_at IS NULL OR k.expires_at>now()))`, buyer, key, group, idempotency).Scan(&exists)
	return exists, err
}

// LookupReplay is intended for use immediately after authentication, before
// catalog, availability, and billing admission checks for new business.
func (r *MarketplaceGatewayRepository) LookupReplay(ctx context.Context, c service.MarketplacePrepareCommand) (*service.MarketplaceReceipt, error) {
	return lookupMarketplaceReplay(ctx, r.db, c)
}

func (r *MarketplaceGatewayRepository) PrepareAndReserve(ctx context.Context, c service.MarketplacePrepareCommand, budget service.Money) (*service.MarketplaceReceipt, bool, error) {
	if c.BuyerUserID <= 0 || c.APIKeyID <= 0 || c.GroupID <= 0 || strings.TrimSpace(c.RequestDigest) == "" || !validMoney(budget) || (c.IdempotencyKey != nil && (strings.TrimSpace(*c.IdempotencyKey) == "" || len(*c.IdempotencyKey) > 255)) {
		return nil, false, service.ErrMarketplaceRequestInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if c.IdempotencyKey != nil {
		// Serialize first creation with concurrent retries, including one whose
		// eligibility changes while the original operation is committing.
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("marketplace:request:%d:%d:%s", c.BuyerUserID, c.APIKeyID, *c.IdempotencyKey)); err != nil {
			return nil, false, err
		}
		existing, e := lookupMarketplaceReplay(ctx, tx, c)
		if e != nil {
			return nil, false, e
		}
		if existing != nil {
			if err = tx.Commit(); err != nil {
				return nil, false, err
			}
			return existing, false, nil
		}
	}
	if err = lockMarketplaceBuyer(ctx, tx, c.BuyerUserID, c.APIKeyID, c.GroupID); err != nil {
		return nil, false, err
	}
	receipt, err := scanMarketplaceReceipt(tx.QueryRowContext(ctx, `INSERT INTO marketplace_requests(id,buyer_user_id,api_key_id,group_id,request_digest,idempotency_key) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (buyer_user_id,api_key_id,idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING RETURNING `+receiptColumns, uuid.NewString(), c.BuyerUserID, c.APIKeyID, c.GroupID, c.RequestDigest, c.IdempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		receipt, err = scanMarketplaceReceipt(tx.QueryRowContext(ctx, `SELECT `+receiptColumns+` FROM marketplace_requests WHERE buyer_user_id=$1 AND api_key_id=$2 AND idempotency_key=$3`, c.BuyerUserID, c.APIKeyID, c.IdempotencyKey))
		if err != nil {
			return nil, false, err
		}
		if receipt.RequestDigest != c.RequestDigest || receipt.GroupID != c.GroupID {
			return nil, false, service.ErrMarketplaceIdempotencyConflict
		}
		if err = tx.Commit(); err != nil {
			return nil, false, err
		}
		return receipt, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err = increaseMarketplaceHold(ctx, tx, c.BuyerUserID, receipt.ID, decimal.Zero, budget.Decimal); err != nil {
		return nil, false, err
	}
	if err = NewMarketplaceRequestRepository(r.db).Reserve(ctx, tx, receipt.ID, budget); err != nil {
		return nil, false, err
	}
	receipt.Status = "budget_reserved"
	receipt.Budget = budget
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return receipt, true, nil
}

func (r *MarketplaceGatewayRepository) qualify(ctx context.Context, tx *sql.Tx, account, buyer, group int64, model string) (int64, int64, decimal.Decimal, error) {
	if r.cfg == nil || !r.cfg.Marketplace.Enabled || !r.cfg.Marketplace.DispatchEnabled || account <= 0 || buyer <= 0 || group <= 0 || strings.TrimSpace(model) == "" {
		return 0, 0, decimal.Zero, service.ErrMarketplaceAccountIneligible
	}
	var offer, owner int64
	var platform, typ string
	var rate decimal.Decimal
	var ok bool
	var extra []byte
	err := tx.QueryRowContext(ctx, `SELECT o.id,o.owner_user_id,a.platform,a.type,a.rate_multiplier,a.extra, (p.status='active' AND owner.status='active' AND owner.deleted_at IS NULL AND a.concurrency>0 AND a.concurrency<=policy.max_supplier_concurrency AND o.review_status='approved' AND o.enabled AND o.revoked_at IS NULL AND a.status='active' AND a.deleted_at IS NULL AND a.schedulable AND a.parent_account_id IS NULL AND (NOT a.auto_pause_on_expired OR a.expires_at IS NULL OR a.expires_at>now()) AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at<=now()) AND (a.overload_until IS NULL OR a.overload_until<=now()) AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until<=now()) AND o.owner_user_id<>$2 AND o.allowed_models ? $4) FROM accounts a JOIN sharing_offers o ON o.account_id=a.id AND o.owner_user_id=a.owner_user_id JOIN supplier_profiles p ON p.user_id=o.owner_user_id JOIN users owner ON owner.id=o.owner_user_id JOIN marketplace_policies policy ON policy.version=(SELECT max(p2.version) FROM marketplace_policies p2 JOIN marketplace_price_versions v2 ON v2.version=p2.version WHERE p2.published_at IS NOT NULL AND v2.published_at IS NOT NULL) JOIN account_groups ag ON ag.account_id=a.id AND ag.group_id=$3 WHERE a.id=$1 FOR SHARE OF a,o,p,ag,owner,policy`, account, buyer, group, model).Scan(&offer, &owner, &platform, &typ, &rate, &extra, &ok)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, decimal.Zero, service.ErrMarketplaceAccountIneligible
	}
	if err != nil {
		return 0, 0, decimal.Zero, err
	}
	var data map[string]any
	if err = json.Unmarshal(extra, &data); err != nil {
		return 0, 0, decimal.Zero, err
	}
	a := service.Account{Type: typ, Extra: data}
	if a.SupportsAccountQuota() && a.IsQuotaExceeded() {
		ok = false
	}
	allowed := false
	for _, p := range r.cfg.Marketplace.Providers {
		if p.Platform != platform {
			continue
		}
		var typOK, modelOK bool
		for _, x := range p.AccountTypes {
			if x == typ {
				typOK = true
			}
		}
		for _, x := range p.Models {
			if x == model {
				modelOK = true
			}
		}
		if typOK && modelOK {
			allowed = true
			break
		}
	}
	if !ok || !allowed {
		return 0, 0, decimal.Zero, service.ErrMarketplaceAccountIneligible
	}
	return offer, owner, rate, nil
}
func (r *MarketplaceGatewayRepository) Qualify(ctx context.Context, account, buyer, group int64, model string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	offer, _, _, err := r.qualify(ctx, tx, account, buyer, group, model)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return offer, nil
}

// validateMarketplaceDispatchQuote binds every commercial term to an immutable published version.
func validateMarketplaceDispatchQuote(ctx context.Context, tx *sql.Tx, buyer, group int64, q service.QuoteSnapshot) error {
	if q.PublishedRateMultiplier == nil || q.ConsumerRateMultiplier == nil {
		return service.ErrMarketplacePriceUnavailable
	}
	var g service.Group
	var base float64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT rate_multiplier FROM user_group_rate_multipliers WHERE user_id=$1 AND group_id=g.id),g.rate_multiplier),g.subscription_type,g.peak_rate_enabled,g.peak_start,g.peak_end,g.peak_rate_multiplier FROM groups g WHERE g.id=$2 AND g.deleted_at IS NULL`, buyer, group).Scan(&base, &g.SubscriptionType, &g.PeakRateEnabled, &g.PeakStart, &g.PeakEnd, &g.PeakRateMultiplier)
	if err != nil {
		return err
	}
	consumer := base * g.PeakMultiplierAt(time.Now())
	if math.IsNaN(consumer) || math.IsInf(consumer, 0) || consumer < 0 || !q.ConsumerRateMultiplier.Equal(decimal.NewFromFloat(consumer)) {
		return service.ErrMarketplacePriceUnavailable
	}
	var body []byte
	var fee, hold int
	err = tx.QueryRowContext(ctx, `SELECT p.rules,s.fee_bps,s.hold_hours FROM marketplace_price_versions p JOIN marketplace_policies s ON s.version=p.version WHERE p.version=$1 AND s.version=$2 AND p.published_at IS NOT NULL AND s.published_at IS NOT NULL AND p.version=(SELECT max(p2.version) FROM marketplace_price_versions p2 JOIN marketplace_policies s2 ON s2.version=p2.version WHERE p2.published_at IS NOT NULL AND s2.published_at IS NOT NULL) FOR SHARE OF p,s`, q.PriceVersion, q.PolicyVersion).Scan(&body, &fee, &hold)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrMarketplacePriceUnavailable
	}
	if err != nil {
		return err
	}
	var book service.MarketplacePriceBook
	if err = json.Unmarshal(body, &book); err != nil {
		return err
	}
	for _, g := range book.Groups {
		if g.GroupID != group || g.Protocol != q.Protocol {
			continue
		}
		for _, m := range g.Models {
			if m.PublicModel != q.PublicModel || m.ActualModel != q.ActualModel {
				continue
			}
			expected := q
			expected.Currency = "USD"
			if m.RateMultiplier == nil || !q.PublishedRateMultiplier.Equal(*m.RateMultiplier) {
				return service.ErrMarketplacePriceUnavailable
			}
			effective := m.RateMultiplier.Mul(decimal.NewFromFloat(consumer))
			expected.RateMultiplier = &effective
			expected.PublishedRateMultiplier = m.RateMultiplier
			consumerDecimal := decimal.NewFromFloat(consumer)
			expected.ConsumerRateMultiplier = &consumerDecimal
			expected.Tiers = m.Tiers
			expected.FeeBPS = fee
			expected.HoldHours = hold
			actualDigest, err := service.MarketplaceQuoteDigest(q)
			if err != nil {
				return err
			}
			expectedDigest, err := service.MarketplaceQuoteDigest(expected)
			if err != nil {
				return err
			}
			if actualDigest != expectedDigest {
				return service.ErrMarketplacePriceUnavailable
			}
			return nil
		}
	}
	return service.ErrMarketplacePriceUnavailable
}

func (r *MarketplaceGatewayRepository) Dispatch(ctx context.Context, id string, q service.QuoteSnapshot) (*service.MarketplaceAttempt, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize with publication, which takes this lock before touching groups.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(738241016260)`); err != nil {
		return nil, err
	}
	receipt, err := scanMarketplaceReceipt(tx.QueryRowContext(ctx, `SELECT `+receiptColumns+` FROM marketplace_requests WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	if err = lockMarketplaceBuyer(ctx, tx, receipt.BuyerUserID, receipt.APIKeyID, receipt.GroupID); err != nil {
		return nil, err
	}
	receipt, err = lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if receipt.Status != "budget_reserved" || receipt.BillableAttemptID != nil {
		return nil, service.ErrMarketplaceRequestState
	}
	if err = validateMarketplaceDispatchQuote(ctx, tx, receipt.BuyerUserID, receipt.GroupID, q); err != nil {
		return nil, err
	}
	offer, owner, rate, err := r.qualify(ctx, tx, q.AccountID, receipt.BuyerUserID, receipt.GroupID, q.ActualModel)
	if err != nil {
		return nil, err
	}
	if offer != q.OfferID || owner != q.OwnerUserID || q.AccountRateMultiplier == nil || !q.AccountRateMultiplier.Equal(rate) {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	requests := NewMarketplaceRequestRepository(r.db)
	a, err := requests.CreateAttempt(ctx, tx, id, q)
	if err != nil {
		return nil, err
	}
	if err = requests.MarkDispatched(ctx, tx, id, a.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.Status = "possibly_executed"
	return a, nil
}
func (r *MarketplaceGatewayRepository) Complete(ctx context.Context, id string, attempt int64, q service.QuoteSnapshot, u service.UsageSnapshot, b service.UsageBillingCommand) (*service.MarketplaceBillingResult, error) {
	digest, err := service.MarketplaceQuoteDigest(q)
	if err != nil {
		return nil, err
	}
	charge, err := service.NewMarketplaceQuoteService().CalculateActual(q, u)
	if err != nil {
		return nil, err
	}
	net, fee, err := service.SplitMarketplaceCharge(charge, q.FeeBPS)
	if err != nil {
		return nil, err
	}
	return NewMarketplaceBillingRepository(r.db).Capture(ctx, service.MarketplaceCaptureCommand{ReceiptID: id, BillableAttemptID: attempt, QuoteDigest: digest, Usage: u, ConsumerCharge: charge, SupplierNet: net, PlatformFee: fee}, b)
}
func (r *MarketplaceGatewayRepository) MarkReview(ctx context.Context, id string, attempt int64, reason string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = NewMarketplaceRequestRepository(r.db).MarkNeedsReview(ctx, tx, id, attempt, reason); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *MarketplaceGatewayRepository) VoidUndispatched(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	receipt, err := scanMarketplaceReceipt(tx.QueryRowContext(ctx, `SELECT `+receiptColumns+` FROM marketplace_requests WHERE id=$1`, id))
	if err != nil {
		return err
	}
	if _, _, _, _, err = fundingLock(ctx, tx, receipt.BuyerUserID); err != nil {
		return err
	}
	receipt, err = lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = NewMarketplaceRequestRepository(r.db).MarkVoided(ctx, tx, id); err != nil {
		return err
	}
	if receipt.Budget.IsPositive() {
		if err = NewWalletFundingRepository().SettleHold(ctx, tx, receipt.BuyerUserID, "marketplace:void:"+id, marketplaceActor, marketplaceHoldID(id), decimal.Zero); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Resolve implements the administration handler's recovery port.
func (r *MarketplaceGatewayRepository) Resolve(ctx context.Context, actor int64, receiptID, action, evidence, operationID string) error {
	return r.AdminRecoveryResolve(ctx, actor, receiptID, action, evidence, operationID)
}

// AdminRecoveryResolve records an operator decision in the same transaction as
// the financial transition. It never contacts an upstream provider.
func (r *MarketplaceGatewayRepository) AdminRecoveryResolve(ctx context.Context, actor int64, receiptID, action, evidence, operationID string) error {
	if actor <= 0 || strings.TrimSpace(evidence) == "" || strings.TrimSpace(operationID) == "" || len(operationID) > 255 || (action != "release_confirmed_unexecuted" && action != "retry_known_usage") {
		return service.ErrMarketplaceRequestInvalid
	}
	if _, err := uuid.Parse(receiptID); err != nil {
		return service.ErrMarketplaceRequestInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var admin bool
	err = tx.QueryRowContext(ctx, `SELECT role='admin' AND status='active' AND deleted_at IS NULL FROM users WHERE id=$1 FOR SHARE`, actor).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !admin) {
		return service.ErrMarketplaceRequestInvalid
	}
	if err != nil {
		return err
	}
	// Acquire funding before the audit's receipt foreign-key lock, matching
	// normal capture's lock order and avoiding a settlement deadlock.
	var buyer int64
	if err = tx.QueryRowContext(ctx, `SELECT buyer_user_id FROM marketplace_requests WHERE id=$1`, receiptID).Scan(&buyer); err != nil {
		return err
	}
	if _, _, _, _, err = fundingLock(ctx, tx, buyer); err != nil {
		return err
	}
	// Unique operation IDs serialize concurrent replays; conflicting intent fails.
	var inserted string
	err = tx.QueryRowContext(ctx, `INSERT INTO marketplace_recovery_audits(operation_id,actor_user_id,receipt_id,action,evidence) VALUES($1,$2,$3,$4,$5) ON CONFLICT(operation_id) DO NOTHING RETURNING operation_id`, operationID, actor, receiptID, action, evidence).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		var oldActor int64
		var oldReceipt, oldAction, oldEvidence string
		if err = tx.QueryRowContext(ctx, `SELECT actor_user_id,receipt_id::text,action,evidence FROM marketplace_recovery_audits WHERE operation_id=$1`, operationID).Scan(&oldActor, &oldReceipt, &oldAction, &oldEvidence); err != nil {
			return err
		}
		if oldActor != actor || oldReceipt != receiptID || oldAction != action || oldEvidence != evidence {
			return service.ErrMarketplaceIdempotencyConflict
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if action == "release_confirmed_unexecuted" {
		receipt, err := lockMarketplaceReceipt(ctx, tx, receiptID)
		if err != nil {
			return err
		}
		if receipt.BillableAttemptID == nil {
			// Only the stale scanner's review of an unattempted receipt can be
			// released here. The receipt lock prevents a concurrent new attempt.
			var reason sql.NullString
			var hasAttemptOrCapture bool
			if err = tx.QueryRowContext(ctx, `SELECT review_reason, EXISTS(SELECT 1 FROM marketplace_attempts WHERE receipt_id=$1) OR EXISTS(SELECT 1 FROM marketplace_captures WHERE receipt_id=$1) FROM marketplace_requests WHERE id=$1`, receiptID).Scan(&reason, &hasAttemptOrCapture); err != nil {
				return err
			}
			if receipt.Status != "needs_review" || !reason.Valid || reason.String != marketplaceStaleReviewReason || hasAttemptOrCapture {
				return service.ErrMarketplaceRequestState
			}
			if receipt.Budget.IsPositive() {
				if err = NewWalletFundingRepository().SettleHold(ctx, tx, buyer, "marketplace:void:"+receiptID, marketplaceActor, marketplaceHoldID(receiptID), decimal.Zero); err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='voided',review_reason=NULL,updated_at=now() WHERE id=$1`, receiptID); err != nil {
				return err
			}
			return tx.Commit()
		}
		if receipt.Status != "needs_review" && receipt.Status != "usage_pending" && receipt.Status != "dispatched" {
			return service.ErrMarketplaceRequestState
		}
		status, _, usage, err := lockMarketplaceAttempt(ctx, tx, receiptID, *receipt.BillableAttemptID)
		if err != nil {
			return err
		}
		if status != "possibly_executed" || len(usage) != 0 {
			return service.ErrMarketplaceRequestState
		}
		var hasEvidence bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_attempts WHERE receipt_id=$1 AND (usage IS NOT NULL OR status IN ('usage_recorded','settled'))) OR EXISTS(SELECT 1 FROM marketplace_captures WHERE receipt_id=$1)`, receiptID).Scan(&hasEvidence); err != nil {
			return err
		}
		if hasEvidence {
			return service.ErrMarketplaceRequestState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status='rejected_before_execution',updated_at=now() WHERE id=$1`, *receipt.BillableAttemptID); err != nil {
			return err
		}
		if receipt.Budget.IsPositive() {
			if err = NewWalletFundingRepository().SettleHold(ctx, tx, buyer, "marketplace:void:"+receiptID, marketplaceActor, marketplaceHoldID(receiptID), decimal.Zero); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='voided',billable_attempt_id=NULL,review_reason=NULL,updated_at=now() WHERE id=$1`, receiptID); err != nil {
			return err
		}
	} else {
		// Read only persisted usage; captureTx locks and revalidates the receipt,
		// attempt, quote digest and usage before applying any financial effects.
		var attempt int64
		var status, digest string
		var quoteJSON, usageJSON []byte
		err = tx.QueryRowContext(ctx, `SELECT a.id,a.status,a.quote_digest,a.quote,a.usage FROM marketplace_requests r JOIN marketplace_attempts a ON a.id=r.billable_attempt_id AND a.receipt_id=r.id WHERE r.id=$1`, receiptID).Scan(&attempt, &status, &digest, &quoteJSON, &usageJSON)
		if err != nil {
			return err
		}
		if status != "usage_recorded" || len(usageJSON) == 0 {
			return service.ErrMarketplaceRequestState
		}
		var q service.QuoteSnapshot
		var u service.UsageSnapshot
		if err = json.Unmarshal(quoteJSON, &q); err != nil {
			return err
		}
		if err = json.Unmarshal(usageJSON, &u); err != nil {
			return err
		}
		charge, err := service.NewMarketplaceQuoteService().CalculateActual(q, u)
		if err != nil {
			return err
		}
		net, fee, err := service.SplitMarketplaceCharge(charge, q.FeeBPS)
		if err != nil {
			return err
		}
		result, err := NewMarketplaceBillingRepository(r.db).captureTx(ctx, tx, service.MarketplaceCaptureCommand{ReceiptID: receiptID, BillableAttemptID: attempt, QuoteDigest: digest, Usage: u, ConsumerCharge: charge, SupplierNet: net, PlatformFee: fee}, service.UsageBillingCommand{})
		if err != nil {
			return err
		}
		if result.NeedsReview {
			return service.ErrMarketplaceRequestState
		}
	}
	return tx.Commit()
}
