package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var ErrPayoutInvalid = errors.New("invalid payout request or transition")

// ValidatePayoutAmount accepts only positive USD amounts with cent precision.
func ValidatePayoutAmount(amount, minimum decimal.Decimal) error {
	if !amount.IsPositive() || minimum.IsNegative() || amount.Exponent() < -2 || !amount.Equal(amount.Round(2)) || amount.LessThan(minimum) {
		return ErrPayoutInvalid
	}
	return nil
}

// PayoutRelease splits a release into debt repayment and available earnings.
func PayoutRelease(amount, outstandingDebt decimal.Decimal) (decimal.Decimal, decimal.Decimal) {
	paid := decimal.Min(amount, outstandingDebt)
	return paid, amount.Sub(paid)
}

type PayoutDestination struct {
	ID          int64     `json:"id"`
	SupplierID  int64     `json:"supplier_id"`
	MaskedLabel string    `json:"masked_label"`
	Version     int64     `json:"version"`
	Verified    bool      `json:"verified"`
	CreatedAt   time.Time `json:"created_at"`
}
type AdminPayoutDestination struct {
	PayoutDestination
	VerifiedBy           *int64     `json:"verified_by,omitempty"`
	VerifiedAt           *time.Time `json:"verified_at,omitempty"`
	VerificationEvidence *string    `json:"verification_evidence,omitempty"`
}
type SupplierPayout struct {
	ID                int64           `json:"id"`
	SupplierID        int64           `json:"supplier_id"`
	DestinationID     int64           `json:"destination_id"`
	MaskedLabel       string          `json:"masked_label"`
	Amount            decimal.Decimal `json:"amount"`
	Currency          string          `json:"currency"`
	Status            string          `json:"status"`
	ExternalReference *string         `json:"external_reference,omitempty"`
	PaidAt            *time.Time      `json:"paid_at,omitempty"`
	Evidence          *string         `json:"evidence,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}
type PayoutAction struct {
	PayoutID          int64
	ActorID           int64
	OperationID       string
	Action            string
	Evidence          string
	ExternalReference string
	Currency          string
	Amount            decimal.Decimal
	PaidAt            *time.Time
}

type SupplierPayoutStore interface {
	CreateDestination(context.Context, int64, string, []byte, string, *CredentialEnvelope) (*PayoutDestination, error)
	VerifyDestination(context.Context, int64, int64, string, string) (*PayoutDestination, error)
	ListAdminDestinations(context.Context, int64) ([]AdminPayoutDestination, error)
	RevealDestination(context.Context, int64, int64, string, string, *CredentialEnvelope) ([]byte, error)
	ListDestinations(context.Context, int64) ([]PayoutDestination, error)
	Request(context.Context, int64, int64, decimal.Decimal, string) (*SupplierPayout, error)
	FindRequest(context.Context, int64, int64, decimal.Decimal, string) (*SupplierPayout, error)
	Action(context.Context, PayoutAction) (*SupplierPayout, error)
	List(context.Context, int64) ([]SupplierPayout, error)
	ListAdmin(context.Context, int64) ([]SupplierPayout, error)
	Get(context.Context, int64, int64, bool) (*SupplierPayout, error)
}
type SupplierPayoutService struct {
	store    SupplierPayoutStore
	policy   *MarketplacePolicyService
	envelope *CredentialEnvelope
}

func NewSupplierPayoutService(store SupplierPayoutStore, policy *MarketplacePolicyService, envelope *CredentialEnvelope) *SupplierPayoutService {
	return &SupplierPayoutService{store: store, policy: policy, envelope: envelope}
}
func (s *SupplierPayoutService) CreateDestination(ctx context.Context, actor int64, mask string, secret []byte, operation string) (*PayoutDestination, error) {
	if actor <= 0 || strings.TrimSpace(mask) == "" || len(secret) == 0 || strings.Contains(mask, string(secret)) || strings.TrimSpace(operation) == "" || s.envelope == nil {
		return nil, ErrPayoutInvalid
	}
	return s.store.CreateDestination(ctx, actor, mask, secret, operation, s.envelope)
}
func (s *SupplierPayoutService) VerifyDestination(ctx context.Context, id, actor int64, operation, evidence string) (*PayoutDestination, error) {
	if id <= 0 || actor <= 0 || strings.TrimSpace(operation) == "" || strings.TrimSpace(evidence) == "" {
		return nil, ErrPayoutInvalid
	}
	return s.store.VerifyDestination(ctx, id, actor, operation, evidence)
}
func (s *SupplierPayoutService) Destinations(ctx context.Context, owner int64) ([]PayoutDestination, error) {
	return s.store.ListDestinations(ctx, owner)
}
func (s *SupplierPayoutService) Request(ctx context.Context, actor, destination int64, amount decimal.Decimal, operation string) (*SupplierPayout, error) {
	if actor <= 0 || destination <= 0 || strings.TrimSpace(operation) == "" || ValidatePayoutAmount(amount, decimal.Zero) != nil {
		return nil, ErrPayoutInvalid
	}
	if s.policy == nil || !s.policy.PayoutsEnabled() || s.policy.cfg.Marketplace.MinimumPayoutUSD == nil {
		return s.store.FindRequest(ctx, actor, destination, amount, operation)
	}
	minimum, err := decimal.NewFromString(*s.policy.cfg.Marketplace.MinimumPayoutUSD)
	if err != nil || ValidatePayoutAmount(amount, minimum) != nil {
		return s.store.FindRequest(ctx, actor, destination, amount, operation)
	}
	return s.store.Request(ctx, actor, destination, amount, operation)
}
func (s *SupplierPayoutService) Action(ctx context.Context, action PayoutAction) (*SupplierPayout, error) {
	if action.ActorID <= 0 || action.PayoutID <= 0 || strings.TrimSpace(action.OperationID) == "" {
		return nil, ErrPayoutInvalid
	}
	return s.store.Action(ctx, action)
}
func (s *SupplierPayoutService) List(ctx context.Context, owner int64) ([]SupplierPayout, error) {
	return s.store.List(ctx, owner)
}
func (s *SupplierPayoutService) Get(ctx context.Context, id, actor int64, admin bool) (*SupplierPayout, error) {
	return s.store.Get(ctx, id, actor, admin)
}

func (s *SupplierPayoutService) ListAdmin(ctx context.Context, actor int64) ([]SupplierPayout, error) {
	return s.store.ListAdmin(ctx, actor)
}

func (s *SupplierPayoutService) AdminDestinations(ctx context.Context, actor int64) ([]AdminPayoutDestination, error) {
	return s.store.ListAdminDestinations(ctx, actor)
}
func (s *SupplierPayoutService) RevealDestination(ctx context.Context, id, actor int64, reason, operation string) ([]byte, error) {
	if id <= 0 || actor <= 0 || strings.TrimSpace(reason) == "" || strings.TrimSpace(operation) == "" || s.envelope == nil {
		return nil, ErrPayoutInvalid
	}
	return s.store.RevealDestination(ctx, id, actor, reason, operation, s.envelope)
}
