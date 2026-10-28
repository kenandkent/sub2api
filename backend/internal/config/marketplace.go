package config

import (
	"fmt"
	"regexp"

	"github.com/shopspring/decimal"
)

// MarketplaceConfig contains opt-in switches and explicit commercial terms.
// Pointer terms distinguish an omitted setting from a deliberate zero.
type MarketplaceConfig struct {
	AgreementText          string   `mapstructure:"agreement_text" yaml:"agreement_text"`
	AgreementURL           string   `mapstructure:"agreement_url" yaml:"agreement_url"`
	AgreementVersion       int64    `mapstructure:"agreement_version" yaml:"agreement_version"`
	ApplicationFields      []string `mapstructure:"application_fields" yaml:"application_fields"`
	Enabled                bool     `mapstructure:"enabled" yaml:"enabled"`
	OnboardingEnabled      bool     `mapstructure:"onboarding_enabled" yaml:"onboarding_enabled"`
	DispatchEnabled        bool     `mapstructure:"dispatch_enabled" yaml:"dispatch_enabled"`
	PayoutsEnabled         bool     `mapstructure:"payouts_enabled" yaml:"payouts_enabled"`
	FeeBPS                 *int     `mapstructure:"fee_bps" yaml:"fee_bps"`
	HoldHours              *int     `mapstructure:"hold_hours" yaml:"hold_hours"`
	MaxSupplierConcurrency *int     `mapstructure:"max_supplier_concurrency" yaml:"max_supplier_concurrency"`
	MinimumPayoutUSD       *string  `mapstructure:"minimum_payout_usd" yaml:"minimum_payout_usd"`
}

var marketplaceMinimumPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

func (c MarketplaceConfig) Validate(runMode string) error {
	if !c.Enabled {
		return nil
	}
	if NormalizeRunMode(runMode) == RunModeSimple {
		return fmt.Errorf("marketplace is unavailable in simple mode")
	}
	if c.FeeBPS == nil || *c.FeeBPS < 0 || *c.FeeBPS > 10000 {
		return fmt.Errorf("marketplace.fee_bps must be explicitly set between 0 and 10000")
	}
	if c.HoldHours == nil || *c.HoldHours < 0 || *c.HoldHours > 87600 {
		return fmt.Errorf("marketplace.hold_hours must be explicitly set between 0 and 87600")
	}
	if c.MaxSupplierConcurrency == nil || *c.MaxSupplierConcurrency <= 0 {
		return fmt.Errorf("marketplace.max_supplier_concurrency must be explicitly set to a positive integer")
	}
	if c.MinimumPayoutUSD == nil || !marketplaceMinimumPattern.MatchString(*c.MinimumPayoutUSD) {
		return fmt.Errorf("marketplace.minimum_payout_usd must be explicitly set to a nonnegative amount with at most two decimal places")
	}
	amount, err := decimal.NewFromString(*c.MinimumPayoutUSD)
	if err != nil || amount.GreaterThanOrEqual(decimal.NewFromInt(1000000000000)) {
		return fmt.Errorf("marketplace.minimum_payout_usd exceeds numeric(20,8)")
	}
	return nil
}
