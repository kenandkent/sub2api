package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

type MarketplaceBillingRepository struct{ db *sql.DB }

func NewMarketplaceBillingRepository(db *sql.DB) *MarketplaceBillingRepository {
	return &MarketplaceBillingRepository{db: db}
}

const marketplaceActor = "marketplace"

func marketplaceHoldID(id string) string { return "marketplace:hold:" + id }

// increaseMarketplaceHold reserves an increment into the original hold's allocations.
// It checks eligibility before writing, allowing an insufficient result to be recorded
// as needs_review in the surrounding transaction.
func increaseMarketplaceHold(ctx context.Context, tx *sql.Tx, user int64, id string, old, amount decimal.Decimal) error {
	if !amount.IsPositive() {
		return nil
	}
	balance, _, debt, lots, err := fundingLock(ctx, tx, user)
	if err != nil {
		return err
	}
	if debt.IsPositive() || balance.LessThan(amount) {
		return service.ErrInsufficientFunding
	}
	portions, err := service.PlanFundingConsumption(lots, amount, true)
	if err != nil {
		return service.ErrInsufficientFunding
	}
	hold := marketplaceHoldID(id)
	if old.IsZero() {
		return NewWalletFundingRepository().Hold(ctx, tx, user, hold, marketplaceActor, amount)
	}
	op := fmt.Sprintf("marketplace:hold-increase:%s:%s", id, old.Add(amount).StringFixed(8))
	replay, err := fundingOperation(ctx, tx, user, op, marketplaceActor, "hold_increase", hold+":"+amount.StringFixed(8))
	if err != nil || replay {
		return err
	}
	for _, p := range portions {
		if _, err = tx.ExecContext(ctx, `UPDATE wallet_funding_lots SET reserved=reserved+$2 WHERE id=$1`, p.LotID, p.Amount); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO wallet_funding_allocations(lot_id,hold_operation_id,reserved) VALUES($1,$2,$3) ON CONFLICT(hold_operation_id,lot_id) DO UPDATE SET reserved=wallet_funding_allocations.reserved+EXCLUDED.reserved`, p.LotID, hold, p.Amount); err != nil {
			return err
		}
	}
	if err = fundingUserUpdate(ctx, tx, `UPDATE users SET balance=balance-$2,frozen_balance=frozen_balance+$2 WHERE id=$1`, user, amount); err != nil {
		return err
	}
	return fundingPost(ctx, tx, user, op, marketplaceActor, "buyer_available", "buyer_frozen", amount)
}

func (r *MarketplaceBillingRepository) ReserveBudget(ctx context.Context, id string, buyer int64, budget service.Money) error {
	if !validMoney(budget) || buyer <= 0 {
		return service.ErrMarketplaceRequestInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, _, _, err = fundingLock(ctx, tx, buyer); err != nil {
		return err
	}
	receipt, err := lockMarketplaceReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	if receipt.BuyerUserID != buyer || (receipt.Status != "prepared" && receipt.Status != "budget_reserved") || budget.LessThan(receipt.Budget.Decimal) {
		return service.ErrMarketplaceRequestState
	}
	if err = increaseMarketplaceHold(ctx, tx, buyer, id, receipt.Budget.Decimal, budget.Sub(receipt.Budget.Decimal)); err != nil {
		return err
	}
	if err = NewMarketplaceRequestRepository(r.db).Reserve(ctx, tx, id, budget); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *MarketplaceBillingRepository) Capture(ctx context.Context, c service.MarketplaceCaptureCommand, b service.UsageBillingCommand) (*service.MarketplaceBillingResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.captureTx(ctx, tx, c, b)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// captureTx allows recovery audit and settlement to commit together.
func (r *MarketplaceBillingRepository) captureTx(ctx context.Context, tx *sql.Tx, c service.MarketplaceCaptureCommand, _ service.UsageBillingCommand) (*service.MarketplaceBillingResult, error) {
	if c.ReceiptID == "" || c.BillableAttemptID <= 0 || c.QuoteDigest == "" {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	expectedOp := "marketplace:capture:" + c.ReceiptID
	if c.OperationID != "" && c.OperationID != expectedOp {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	var err error
	var buyer, owner int64
	if err = tx.QueryRowContext(ctx, `SELECT r.buyer_user_id,a.owner_user_id FROM marketplace_requests r JOIN marketplace_attempts a ON a.receipt_id=r.id WHERE r.id=$1 AND a.id=$2`, c.ReceiptID, c.BillableAttemptID).Scan(&buyer, &owner); err != nil {
		return nil, err
	}
	if _, _, _, _, err = fundingLock(ctx, tx, buyer); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id) VALUES($1) ON CONFLICT(user_id,currency) DO NOTHING`, owner); err != nil {
		return nil, err
	}
	var supplierDebt decimal.Decimal
	if err = tx.QueryRowContext(ctx, `SELECT debt FROM supplier_wallets WHERE user_id=$1 AND currency='USD' FOR UPDATE`, owner).Scan(&supplierDebt); err != nil {
		return nil, err
	}
	receipt, err := lockMarketplaceReceipt(ctx, tx, c.ReceiptID)
	if err != nil {
		return nil, err
	}
	var status, digest, accountType string
	var quoteJSON, usageJSON []byte
	var account, offer, storedOwner int64
	err = tx.QueryRowContext(ctx, `SELECT a.status,a.quote_digest,a.quote,a.usage,a.account_id,a.offer_id,a.owner_user_id,ac.type FROM marketplace_attempts a JOIN accounts ac ON ac.id=a.account_id WHERE a.receipt_id=$1 AND a.id=$2 FOR UPDATE OF a`, c.ReceiptID, c.BillableAttemptID).Scan(&status, &digest, &quoteJSON, &usageJSON, &account, &offer, &storedOwner, &accountType)
	if err != nil {
		return nil, err
	}
	var q service.QuoteSnapshot
	if err = json.Unmarshal(quoteJSON, &q); err != nil {
		return nil, err
	}
	calculatedDigest, err := service.MarketplaceQuoteDigest(q)
	if err != nil {
		return nil, err
	}
	if digest != c.QuoteDigest || digest != calculatedDigest || q.AccountID != account || q.OfferID != offer || q.OwnerUserID != storedOwner || storedOwner != owner || receipt.BillableAttemptID == nil || *receipt.BillableAttemptID != c.BillableAttemptID {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	if q.AccountRateMultiplier == nil || q.AccountRateMultiplier.IsNegative() {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	charge, err := service.NewMarketplaceQuoteService().CalculateActual(q, c.Usage)
	if err != nil {
		return nil, err
	}
	supplier, fee, err := service.SplitMarketplaceCharge(charge, q.FeeBPS)
	if err != nil {
		return nil, err
	}
	if !charge.Equal(c.ConsumerCharge.Decimal) || !supplier.Equal(c.SupplierNet.Decimal) || !fee.Equal(c.PlatformFee.Decimal) {
		return nil, service.ErrMarketplaceRequestInvalid
	}
	fingerprintData, _ := json.Marshal(struct {
		Attempt int64
		Digest  string
		Usage   service.UsageSnapshot
	}{c.BillableAttemptID, digest, c.Usage})
	sum := sha256.Sum256(fingerprintData)
	fp := hex.EncodeToString(sum[:])
	var existingFP string
	var ledgerID sql.NullInt64
	var quotaExhausted, rateChanged bool
	var balance decimal.Decimal
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,ledger_transaction_id,api_key_quota_exhausted,api_key_rate_limit_changed,buyer_balance_after FROM marketplace_captures WHERE receipt_id=$1`, c.ReceiptID).Scan(&existingFP, &ledgerID, &quotaExhausted, &rateChanged, &balance)
	if err == nil {
		if existingFP != fp {
			return nil, service.ErrUsageBillingRequestConflict
		}
		balanceFloat, _ := balance.Float64()
		return &service.MarketplaceBillingResult{LedgerTransactionID: ledgerID.Int64, APIKeyQuotaExhausted: quotaExhausted, APIKeyRateLimitChanged: rateChanged, NewBalance: &balanceFloat}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if receipt.Status != "dispatched" && receipt.Status != "usage_pending" && receipt.Status != "needs_review" {
		return nil, service.ErrMarketplaceRequestState
	}
	switch status {
	case "possibly_executed":
		data, _ := json.Marshal(c.Usage)
		if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status='usage_recorded',usage=$2,updated_at=now() WHERE id=$1`, c.BillableAttemptID, string(data)); err != nil {
			return nil, err
		}
	case "usage_recorded":
		var existing service.UsageSnapshot
		if err = json.Unmarshal(usageJSON, &existing); err != nil {
			return nil, err
		}
		if existing != c.Usage {
			return nil, service.ErrUsageBillingRequestConflict
		}
	default:
		return nil, service.ErrMarketplaceRequestState
	}
	if charge.GreaterThan(receipt.Budget.Decimal) {
		extra := charge.Sub(receipt.Budget.Decimal)
		err = increaseMarketplaceHold(ctx, tx, buyer, c.ReceiptID, receipt.Budget.Decimal, extra)
		if errors.Is(err, service.ErrInsufficientFunding) {
			if _, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='needs_review',review_reason=$2,updated_at=now() WHERE id=$1`, c.ReceiptID, "insufficient eligible funding; shortfall="+extra.StringFixed(8)); err != nil {
				return nil, err
			}
			return &service.MarketplaceBillingResult{NeedsReview: true}, nil
		}
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET budget=$2 WHERE id=$1`, c.ReceiptID, charge.String()); err != nil {
			return nil, err
		}
	}
	// Quota effects are derived exclusively from persisted attribution and prices.
	accountQuote := q
	accountQuote.RateMultiplier = q.AccountRateMultiplier
	accountCharge, err := service.NewMarketplaceQuoteService().CalculateActual(accountQuote, c.Usage)
	if err != nil {
		return nil, err
	}
	accountFloat, _ := accountCharge.Float64()
	ur := &usageBillingRepository{db: r.db}
	applied, err := ur.claimUsageBillingRequest(ctx, tx, "marketplace:"+c.ReceiptID, receipt.APIKeyID, fp)
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, service.ErrUsageBillingRequestConflict
	}
	if charge.IsPositive() {
		if quotaExhausted, err = incrementUsageBillingAPIKeyQuota(ctx, tx, receipt.APIKeyID, charge.String()); err != nil && !errors.Is(err, service.ErrAPIKeyNotFound) {
			return nil, err
		}
		if err = incrementUsageBillingAPIKeyRateLimit(ctx, tx, receipt.APIKeyID, charge.String()); err != nil {
			if !errors.Is(err, service.ErrAPIKeyNotFound) {
				return nil, err
			}
		} else {
			rateChanged = true
		}
	}
	if accountCharge.IsPositive() && service.AccountTypeSupportsQuota(accountType) {
		if _, err = incrementUsageBillingAccountQuotaExact(ctx, tx, account, accountCharge.String(), accountFloat, true, true); err != nil && !errors.Is(err, service.ErrAccountNotFound) {
			return nil, err
		}
	}
	if receipt.Budget.IsPositive() || charge.IsPositive() {
		if err = NewWalletFundingRepository().SettleHold(ctx, tx, buyer, "marketplace:settle:"+c.ReceiptID, marketplaceActor, marketplaceHoldID(c.ReceiptID), charge.Decimal); err != nil {
			return nil, err
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, buyer).Scan(&balance); err != nil {
		return nil, err
	}
	paid := decimal.Min(supplierDebt, supplier.Decimal)
	pending := supplier.Sub(paid)
	if _, err = tx.ExecContext(ctx, `UPDATE supplier_wallets SET pending=pending+$2,debt=debt-$3,version=version+1 WHERE user_id=$1 AND currency='USD'`, owner, pending, paid); err != nil {
		return nil, err
	}
	if charge.IsPositive() {
		entries := []LedgerPosting{{Account: "marketplace_clearing", Side: "debit", Amount: charge.Decimal}}
		if pending.IsPositive() {
			entries = append(entries, LedgerPosting{Account: "supplier_pending", SubjectID: &owner, Side: "credit", Amount: pending})
		}
		if paid.IsPositive() {
			entries = append(entries, LedgerPosting{Account: "supplier_debt", SubjectID: &owner, Side: "credit", Amount: paid})
		}
		if fee.IsPositive() {
			entries = append(entries, LedgerPosting{Account: "platform_fee", Side: "credit", Amount: fee.Decimal})
		}
		if err = NewMarketplaceLedgerRepository().Post(ctx, tx, expectedOp, marketplaceActor, "USD", entries); err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT id FROM ledger_transactions WHERE operation_id=$1`, expectedOp).Scan(&ledgerID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET status='settled',updated_at=now() WHERE id=$1`, c.BillableAttemptID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_requests SET status='settled',actual_charge=$2,review_reason=NULL,updated_at=now() WHERE id=$1`, c.ReceiptID, charge.String()); err != nil {
		return nil, err
	}
	var captureID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO marketplace_captures(receipt_id,attempt_id,owner_user_id,consumer_charge,supplier_net,platform_fee,supplier_debt_offset,matures_at,ledger_transaction_id,fingerprint,api_key_quota_exhausted,api_key_rate_limit_changed,buyer_balance_after) VALUES($1,$2,$3,$4,$5,$6,$7,now()+($8 * interval '1 hour'),$9,$10,$11,$12,$13) RETURNING id`, c.ReceiptID, c.BillableAttemptID, owner, charge.String(), supplier.String(), fee.String(), paid.String(), q.HoldHours, ledgerID, fp, quotaExhausted, rateChanged, balance.String()).Scan(&captureID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_outbox(event_id,event_type,receipt_id,capture_id) VALUES($1,'marketplace_captured',$2,$3)`, expectedOp, c.ReceiptID, captureID); err != nil {
		return nil, err
	}
	balanceFloat, _ := balance.Float64()
	return &service.MarketplaceBillingResult{FirstApplied: true, LedgerTransactionID: ledgerID.Int64, APIKeyQuotaExhausted: quotaExhausted, APIKeyRateLimitChanged: rateChanged, NewBalance: &balanceFloat}, nil
}
