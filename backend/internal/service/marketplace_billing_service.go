package service

import "context"

// MarketplaceBillingRepository owns the complete financial transaction.
type MarketplaceBillingRepository interface {
	ReserveBudget(context.Context, string, int64, Money) error
	Capture(context.Context, MarketplaceCaptureCommand, UsageBillingCommand) (*MarketplaceBillingResult, error)
}

type MarketplaceBillingResult struct {
	FirstApplied        bool
	NeedsReview         bool
	LedgerTransactionID int64
}

type MarketplaceBillingService struct{ repository MarketplaceBillingRepository }

func NewMarketplaceBillingService(r MarketplaceBillingRepository) *MarketplaceBillingService {
	return &MarketplaceBillingService{repository: r}
}
func (s *MarketplaceBillingService) ReserveBudget(ctx context.Context, receipt string, buyer int64, budget Money) error {
	return s.repository.ReserveBudget(ctx, receipt, buyer, budget)
}
func (s *MarketplaceBillingService) Capture(ctx context.Context, command MarketplaceCaptureCommand, usage UsageBillingCommand) (*MarketplaceBillingResult, error) {
	return s.repository.Capture(ctx, command, usage)
}
