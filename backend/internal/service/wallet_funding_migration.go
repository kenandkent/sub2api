package service

import "context"

// MarketplaceFundingBalance separates usable cash-backed funds from the
// consumer's existing balance, without treating legacy credit as cash.
type MarketplaceFundingBalance struct {
	Currency          string `json:"currency"`
	EligibleAvailable Money  `json:"eligible_available"`
	Reserved          Money  `json:"reserved"`
	ExcludedAvailable Money  `json:"excluded_available"`
	Debt              Money  `json:"debt"`
	Migrated          bool   `json:"migrated"`
	Reconciled        bool   `json:"reconciled"`
}

type WalletFundingMigrationRepository interface {
	MigrateUser(context.Context, int64) error
	GetBalance(context.Context, int64) (*MarketplaceFundingBalance, error)
}

type WalletFundingMigrationService struct{ repo WalletFundingMigrationRepository }

func NewWalletFundingMigrationService(repo WalletFundingMigrationRepository) *WalletFundingMigrationService {
	return &WalletFundingMigrationService{repo: repo}
}

func (s *WalletFundingMigrationService) MigrateUser(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return ErrUserNotFound
	}
	return s.repo.MigrateUser(ctx, userID)
}

func (s *WalletFundingMigrationService) GetBalance(ctx context.Context, userID int64) (*MarketplaceFundingBalance, error) {
	if userID <= 0 {
		return nil, ErrUserNotFound
	}
	return s.repo.GetBalance(ctx, userID)
}
