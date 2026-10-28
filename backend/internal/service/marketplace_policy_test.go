//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type marketplaceTermsFixture struct{ terms MarketplacePolicyVersion }
func (s marketplaceTermsFixture) CurrentPolicy(context.Context) (MarketplacePolicyVersion,error) {return s.terms,nil}

func TestMarketplacePolicyUsesPublishedTermsForNewRequests(t *testing.T) {
	s:=NewMarketplacePolicyService(&config.Config{Marketplace:validMarketplaceConfig()})
	s.SetTermsSource(marketplaceTermsFixture{terms:MarketplacePolicyVersion{Version:2,FeeBPS:500,HoldHours:48,MaxSupplierConcurrency:4,MinimumPayoutUSD:Money{Decimal:decimal.RequireFromString("3.25")}}})
	terms,err:=s.CurrentTerms(context.Background())
	require.NoError(t,err)
	require.EqualValues(t,2,terms.Version)
	require.Equal(t,"3.25",terms.MinimumPayoutUSD.String())
	require.Equal(t,500,terms.FeeBPS)
}

func validMarketplaceConfig() config.MarketplaceConfig {
	fee, hold, concurrency, minimum := 0, 0, 1, "0.00"
	return config.MarketplaceConfig{Enabled: true, FeeBPS: &fee, HoldHours: &hold, MaxSupplierConcurrency: &concurrency, MinimumPayoutUSD: &minimum}
}

func TestMarketplacePolicyDefaultDisabled(t *testing.T) {
	s := NewMarketplacePolicyService(&config.Config{})
	require.False(t, s.OnboardingEnabled())
	require.False(t, s.DispatchEnabled())
	require.False(t, s.PayoutsEnabled())
	require.NoError(t, s.ValidateEnable())
}

func TestMarketplacePolicySimpleModeRejected(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeSimple, Marketplace: validMarketplaceConfig()}
	require.Error(t, NewMarketplacePolicyService(cfg).ValidateEnable())
}

func TestMarketplacePolicyRequiresExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*config.MarketplaceConfig)
	}{
		{"fee", func(c *config.MarketplaceConfig) { c.FeeBPS = nil }},
		{"hold", func(c *config.MarketplaceConfig) { c.HoldHours = nil }},
		{"concurrency", func(c *config.MarketplaceConfig) { c.MaxSupplierConcurrency = nil }},
		{"minimum", func(c *config.MarketplaceConfig) { c.MinimumPayoutUSD = nil }},
		{"invalid fee", func(c *config.MarketplaceConfig) { n := 10001; c.FeeBPS = &n }},
		{"invalid hold", func(c *config.MarketplaceConfig) { n := -1; c.HoldHours = &n }},
		{"invalid concurrency", func(c *config.MarketplaceConfig) { n := 0; c.MaxSupplierConcurrency = &n }},
		{"invalid minimum", func(c *config.MarketplaceConfig) { n := "0.001"; c.MinimumPayoutUSD = &n }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validMarketplaceConfig()
			tc.mutate(&c)
			require.Error(t, NewMarketplacePolicyService(&config.Config{Marketplace: c}).ValidateEnable())
		})
	}
	require.NoError(t, NewMarketplacePolicyService(&config.Config{Marketplace: validMarketplaceConfig()}).ValidateEnable())
}

func TestMarketplacePolicySubswitchesRequireMaster(t *testing.T) {
	c := validMarketplaceConfig()
	c.Enabled = false
	c.OnboardingEnabled = true
	c.DispatchEnabled = true
	c.PayoutsEnabled = true
	s := NewMarketplacePolicyService(&config.Config{Marketplace: c})
	require.False(t, s.OnboardingEnabled())
	require.False(t, s.DispatchEnabled())
	require.False(t, s.PayoutsEnabled())
	c.Enabled = true
	s = NewMarketplacePolicyService(&config.Config{Marketplace: c})
	require.True(t, s.OnboardingEnabled())
	require.True(t, s.DispatchEnabled())
	require.True(t, s.PayoutsEnabled())
}

func TestMarketplacePolicyPublishedVersionImmutable(t *testing.T) {
	now := time.Now()
	original := MarketplacePolicyVersion{Version: 1, FeeBPS: 100, PublishedAt: &now}
	changed := original
	changed.FeeBPS = 200
	require.Error(t, ValidateMarketplacePolicyUpdate(original, changed))
	require.NoError(t, ValidateMarketplacePolicyUpdate(original, original))
	original.PublishedAt = nil
	require.NoError(t, ValidateMarketplacePolicyUpdate(original, changed))
}

func TestMarketplacePolicyMoneyJSONString(t *testing.T) {
	encoded, err := json.Marshal(Money{Decimal: decimal.RequireFromString("12.34567890")})
	require.NoError(t, err)
	require.JSONEq(t, `"12.3456789"`, string(encoded))
	var decoded Money
	require.NoError(t, json.Unmarshal([]byte(`"12.34567890"`), &decoded))
	require.True(t, decoded.Decimal.Equal(decimal.RequireFromString("12.34567890")))
	require.Error(t, json.Unmarshal([]byte(`12.3`), &decoded))
}
