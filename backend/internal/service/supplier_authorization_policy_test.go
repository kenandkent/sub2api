package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type authorizationPolicyProfiles struct {
	SupplierRepository
	profile *SupplierProfile
}

func (p authorizationPolicyProfiles) GetProfile(context.Context, int64) (*SupplierProfile, error) {
	return p.profile, nil
}

type authorizationPolicyAccounts struct {
	SupplierAccountStore
	account *SupplierAccountDetail
	owner   int64
}

func (s authorizationPolicyAccounts) Get(_ context.Context, owner, id int64) (*SupplierAccountDetail, error) {
	if owner != s.owner || s.account == nil || s.account.ID != id {
		return nil, ErrSupplierAuthorizationInvalid
	}
	return s.account, nil
}
func TestSupplierAuthorizationPolicyEnforcesTypeAndOwner(t *testing.T) {
	limit := 4
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, MaxSupplierConcurrency: &limit, Providers: []config.MarketplaceProviderConfig{{Platform: "openai", Label: "OpenAI", BaseURL: "https://api.openai.com/v1", AccountTypes: []string{"apikey"}, Models: []string{"m"}}}}}
	p := authorizationPolicyProfiles{profile: &SupplierProfile{UserID: 7, Status: SupplierStatusActive}}
	a := authorizationPolicyAccounts{owner: 7, account: &SupplierAccountDetail{ID: 8, Platform: "openai", Type: "oauth"}}
	s := NewSupplierAccountService(cfg, a, p, nil, nil)
	require.Error(t, s.AuthorizeSupplierAuthorization(context.Background(), 7, "create", 0))
	cfg.Marketplace.Providers[0].AccountTypes = []string{"apikey", "oauth"}
	require.NoError(t, s.AuthorizeSupplierAuthorization(context.Background(), 7, "create", 0))
	require.NoError(t, s.AuthorizeSupplierAuthorization(context.Background(), 7, "reauthorize", 8))
	require.Error(t, s.AuthorizeSupplierAuthorization(context.Background(), 9, "reauthorize", 8))
	require.Error(t, s.AuthorizeSupplierAuthorization(context.Background(), 7, "create", 8))
	p.profile.Status = SupplierStatusSuspended
	require.Error(t, s.AuthorizeSupplierAuthorization(context.Background(), 7, "reauthorize", 8))
}
