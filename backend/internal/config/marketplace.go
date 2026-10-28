package config

import (
	"fmt"
	"regexp"

	"github.com/shopspring/decimal"
)

// MarketplaceConfig contains opt-in switches and explicit commercial terms.
// Pointer terms distinguish an omitted setting from a deliberate zero.
type MarketplaceProviderConfig struct {
	Platform     string   `mapstructure:"platform" yaml:"platform"`
	Label        string   `mapstructure:"label" yaml:"label"`
	BaseURL      string   `mapstructure:"base_url" yaml:"base_url"`
	AccountTypes []string `mapstructure:"account_types" yaml:"account_types"`
	Models       []string `mapstructure:"models" yaml:"models"`
	ProxyID      *int64   `mapstructure:"proxy_id" yaml:"proxy_id"`
	// Operator attests that the provider enforces this total billable input
	// ceiling, including hidden framing, for every enabled account type.
	MaxBillableInputTokensByModel map[string]int64 `mapstructure:"max_billable_input_tokens_by_model" yaml:"max_billable_input_tokens_by_model"`
}

type MarketplaceConfig struct {
	CredentialKeyID        string                      `mapstructure:"credential_key_id" yaml:"credential_key_id"`
	CredentialKeysJSON     string                      `mapstructure:"credential_keys_json" yaml:"credential_keys_json"`
	UpstreamTimeoutSeconds int                         `mapstructure:"upstream_timeout_seconds" yaml:"upstream_timeout_seconds"`
	OAuthProxyURL          string                      `mapstructure:"oauth_proxy_url" yaml:"oauth_proxy_url"`
	Providers              []MarketplaceProviderConfig `mapstructure:"providers" yaml:"providers"`
	AgreementText          string                      `mapstructure:"agreement_text" yaml:"agreement_text"`
	AgreementURL           string                      `mapstructure:"agreement_url" yaml:"agreement_url"`
	AgreementVersion       int64                       `mapstructure:"agreement_version" yaml:"agreement_version"`
	ApplicationFields      []string                    `mapstructure:"application_fields" yaml:"application_fields"`
	Enabled                bool                        `mapstructure:"enabled" yaml:"enabled"`
	OnboardingEnabled      bool                        `mapstructure:"onboarding_enabled" yaml:"onboarding_enabled"`
	DispatchEnabled        bool                        `mapstructure:"dispatch_enabled" yaml:"dispatch_enabled"`
	PayoutsEnabled         bool                        `mapstructure:"payouts_enabled" yaml:"payouts_enabled"`
	FeeBPS                 *int                        `mapstructure:"fee_bps" yaml:"fee_bps"`
	HoldHours              *int                        `mapstructure:"hold_hours" yaml:"hold_hours"`
	MaxSupplierConcurrency *int                        `mapstructure:"max_supplier_concurrency" yaml:"max_supplier_concurrency"`
	MinimumPayoutUSD       *string                     `mapstructure:"minimum_payout_usd" yaml:"minimum_payout_usd"`
}

var marketplaceMinimumPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

func (c MarketplaceConfig) Validate(runMode string) error {
	if c.UpstreamTimeoutSeconds < 0 || c.UpstreamTimeoutSeconds > 3600 {
		return fmt.Errorf("marketplace.upstream_timeout_seconds must be 0 or between 1 and 3600")
	}
	for _, provider := range c.Providers {
		for model, limit := range provider.MaxBillableInputTokensByModel {
			if model == "" || limit <= 0 || limit > 4000000 {
				return fmt.Errorf("marketplace provider input ceiling must be between 1 and 4000000")
			}
			found := false
			for _, allowed := range provider.Models {
				if allowed == model {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("marketplace provider input ceiling model is not allowed")
			}
		}
	}
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

// AttestedInputCeiling returns the largest provider-enforced input ceiling among
// providers capable of serving this model. Missing attestation fails closed.
func (c MarketplaceConfig) AttestedInputCeiling(platform, model string) (int64, bool) {
	var max int64
	found := false
	for _, p := range c.Providers {
		if p.Platform != platform {
			continue
		}
		allowed := false
		for _, m := range p.Models {
			if m == model {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		found = true
		n := p.MaxBillableInputTokensByModel[model]
		if n <= 0 || n > 4000000 {
			return 0, false
		}
		if n > max {
			max = n
		}
	}
	return max, found
}
