package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Money is an exact decimal value encoded as a JSON string.
type Money struct{ decimal.Decimal }

func (m Money) MarshalJSON() ([]byte, error) { return json.Marshal(m.Decimal.String()) }
func (m *Money) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("money must be a decimal string: %w", err)
	}
	d, err := decimal.NewFromString(value)
	if err != nil {
		return fmt.Errorf("invalid money: %w", err)
	}
	m.Decimal = d
	return nil
}

type SupplierStatus string

const (
	SupplierStatusPending   SupplierStatus = "pending"
	SupplierStatusActive    SupplierStatus = "active"
	SupplierStatusRejected  SupplierStatus = "rejected"
	SupplierStatusSuspended SupplierStatus = "suspended"
)

type OfferReviewStatus string

const (
	OfferReviewDraft    OfferReviewStatus = "draft"
	OfferReviewPending  OfferReviewStatus = "pending_review"
	OfferReviewApproved OfferReviewStatus = "approved"
	OfferReviewRejected OfferReviewStatus = "rejected"
	OfferReviewRevoked  OfferReviewStatus = "revoked"
)

type MarketplaceEligibilityReason string

const (
	EligibilityEligible           MarketplaceEligibilityReason = "eligible"
	EligibilityPaused             MarketplaceEligibilityReason = "paused"
	EligibilityCredentialsInvalid MarketplaceEligibilityReason = "credentials_invalid"
	EligibilityRateLimited        MarketplaceEligibilityReason = "rate_limited"
	EligibilitySupplierSuspended  MarketplaceEligibilityReason = "supplier_suspended"
	EligibilityNotApproved        MarketplaceEligibilityReason = "not_approved"
)

type SupplierCapability struct {
	UserID          int64          `json:"user_id"`
	Status          SupplierStatus `json:"status"`
	CanApply        bool           `json:"can_apply"`
	CanManageOffers bool           `json:"can_manage_offers"`
	CanReadHistory  bool           `json:"can_read_history"`
	PayoutFrozen    bool           `json:"payout_frozen"`
}

// MarketplacePriceTier snapshots the resolved prices per million tokens and the
// inclusive token threshold above which the tier applies.
type MarketplacePriceTier struct {
	FromTokens           int64 `json:"from_tokens"`
	InputPerMillion      Money `json:"input_per_million"`
	OutputPerMillion     Money `json:"output_per_million"`
	CacheReadPerMillion  Money `json:"cache_read_per_million"`
	CacheWritePerMillion Money `json:"cache_write_per_million"`
}
type QuoteSnapshot struct {
	Currency       string                 `json:"currency"`
	PublicModel    string                 `json:"public_model"`
	ActualModel    string                 `json:"actual_model"`
	Protocol       string                 `json:"protocol"`
	PriceVersion   int64                  `json:"price_version"`
	PolicyVersion  int64                  `json:"policy_version"`
	RateMultiplier decimal.Decimal        `json:"rate_multiplier"`
	Tiers          []MarketplacePriceTier `json:"tiers"`
	FeeBPS         int                    `json:"fee_bps"`
	HoldHours      int                    `json:"hold_hours"`
	AccountID      int64                  `json:"account_id"`
	OfferID        int64                  `json:"offer_id"`
	OwnerUserID    int64                  `json:"owner_user_id"`
	SnapshotAt     time.Time              `json:"snapshot_at"`
}
type UsageSnapshot struct {
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Source           string `json:"source"`
}
type MarketplaceReceipt struct {
	ID                string  `json:"id"`
	BuyerUserID       int64   `json:"buyer_user_id"`
	APIKeyID          int64   `json:"api_key_id"`
	GroupID           int64   `json:"group_id"`
	RequestDigest     string  `json:"request_digest"`
	IdempotencyKey    *string `json:"idempotency_key,omitempty"`
	Status            string  `json:"status"`
	Budget            Money   `json:"budget"`
	ActualCharge      Money   `json:"actual_charge"`
	BillableAttemptID *int64  `json:"billable_attempt_id,omitempty"`
}
type MarketplaceAttempt struct {
	ID          int64          `json:"id"`
	ReceiptID   string         `json:"receipt_id"`
	Ordinal     int            `json:"ordinal"`
	AccountID   int64          `json:"account_id"`
	OfferID     int64          `json:"offer_id"`
	OwnerUserID int64          `json:"owner_user_id"`
	Quote       QuoteSnapshot  `json:"quote"`
	Status      string         `json:"status"`
	Usage       *UsageSnapshot `json:"usage,omitempty"`
}
type MarketplaceCaptureCommand struct {
	ReceiptID         string
	BillableAttemptID int64
	Usage             UsageSnapshot
	QuoteDigest       string
	ConsumerCharge    Money
	SupplierNet       Money
	PlatformFee       Money
	OperationID       string
}
type CaptureResult struct {
	FirstApplied        bool
	LedgerTransactionID int64
}

type MarketplacePolicyVersion struct {
	Version                int64
	FeeBPS                 int
	HoldHours              int
	MaxSupplierConcurrency int
	MinimumPayoutUSD       Money
	Rules                  map[string]any
	PublishedBy            *int64
	PublishedAt            *time.Time
}
