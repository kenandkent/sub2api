package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarketplaceConfigEnvironmentExplicitZeros(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MARKETPLACE_ENABLED", "true")
	t.Setenv("MARKETPLACE_DISPATCH_ENABLED", "true")
	t.Setenv("MARKETPLACE_FEE_BPS", "0")
	t.Setenv("MARKETPLACE_HOLD_HOURS", "0")
	t.Setenv("MARKETPLACE_MAX_SUPPLIER_CONCURRENCY", "3")
	t.Setenv("MARKETPLACE_MINIMUM_PAYOUT_USD", "0.00")
	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Marketplace.Enabled)
	require.True(t, cfg.Marketplace.DispatchEnabled)
	require.NotNil(t, cfg.Marketplace.FeeBPS)
	require.Equal(t, 0, *cfg.Marketplace.FeeBPS)
	require.NotNil(t, cfg.Marketplace.HoldHours)
	require.Equal(t, 0, *cfg.Marketplace.HoldHours)
	require.NotNil(t, cfg.Marketplace.MinimumPayoutUSD)
	require.Equal(t, "0.00", *cfg.Marketplace.MinimumPayoutUSD)
}
func TestMarketplaceConfigRefusesIncompleteEnabledConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MARKETPLACE_ENABLED", "true")
	_, err := Load()
	require.Error(t, err)
}

func TestMarketplaceAgreementEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("MARKETPLACE_AGREEMENT_VERSION", "7")
	t.Setenv("MARKETPLACE_AGREEMENT_TEXT", "Terms")
	t.Setenv("MARKETPLACE_AGREEMENT_URL", "/terms")
	t.Setenv("MARKETPLACE_APPLICATION_FIELDS", "contact,company")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, int64(7), cfg.Marketplace.AgreementVersion)
	require.Equal(t, "Terms", cfg.Marketplace.AgreementText)
	require.Equal(t, "/terms", cfg.Marketplace.AgreementURL)
	require.Equal(t, []string{"contact", "company"}, cfg.Marketplace.ApplicationFields)
}
