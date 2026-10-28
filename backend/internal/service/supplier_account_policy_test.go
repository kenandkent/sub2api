package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type policyAccountCatalog struct {
	max int
	err error
}

func (c *policyAccountCatalog) CurrentPolicy(context.Context) (MarketplacePolicyVersion, error) {
	return MarketplacePolicyVersion{MaxSupplierConcurrency: c.max}, c.err
}
func (c *policyAccountCatalog) Prices(_ context.Context, _ string, models []string) ([]SupplierModelPrice, error) {
	var p []SupplierModelPrice
	for _, m := range models {
		p = append(p, SupplierModelPrice{GroupID: 1, Model: m})
	}
	return p, nil
}

type policyAccountStore struct {
	SupplierAccountStore
	account *SupplierAccountDetail
}

func (s *policyAccountStore) Get(context.Context, int64, int64) (*SupplierAccountDetail, error) {
	return s.account, nil
}
func (s *policyAccountStore) Create(context.Context, int64, string, string, SupplierAccountCreate, config.MarketplaceProviderConfig) (*SupplierOperation, error) {
	return &SupplierOperation{}, nil
}
func (s *policyAccountStore) Patch(context.Context, int64, int64, SupplierAccountPatch) (*SupplierAccountDetail, error) {
	return s.account, nil
}
func (s *policyAccountStore) Transition(context.Context, int64, int64, int64, string) (*SupplierAccountDetail, error) {
	return s.account, nil
}
func (s *policyAccountStore) GetForReview(context.Context, int64) (*SupplierAccountDetail, error) {
	return s.account, nil
}
func (s *policyAccountStore) Review(context.Context, int64, int64, int64, string, string, []int64) (*SupplierAccountDetail, error) {
	return s.account, nil
}
func TestSupplierAccountsEnforcePublishedConcurrency(t *testing.T) {
	ctx := context.Background()
	configured := 10
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, MaxSupplierConcurrency: &configured, Providers: []config.MarketplaceProviderConfig{{Platform: "openai", Label: "OpenAI", BaseURL: "https://api.openai.com/v1", AccountTypes: []string{"apikey"}, Models: []string{"m"}}}}}
	cat := &policyAccountCatalog{max: 2}
	store := &policyAccountStore{account: &SupplierAccountDetail{Platform: "openai", Concurrency: 3, CredentialStatus: "configured", Offer: SupplierOffer{AllowedModels: []string{"m"}}}}
	svc := NewSupplierAccountService(cfg, store, authorizationPolicyProfiles{profile: &SupplierProfile{Status: SupplierStatusActive}}, cat, nil)
	create := SupplierAccountCreate{Name: "example", Platform: "openai", Type: "apikey", AllowedModels: []string{"m"}, Concurrency: 3}
	_, err := svc.Create(ctx, 1, "key", create)
	require.Error(t, err)
	value := 3
	_, err = svc.Patch(ctx, 1, 1, SupplierAccountPatch{ExpectedVersion: 1, Concurrency: &value})
	require.Error(t, err)
	_, err = svc.Patch(ctx, 1, 1, SupplierAccountPatch{ExpectedVersion: 1})
	require.Error(t, err)
	for _, action := range []string{"submit", "resume"} {
		_, err = svc.Transition(ctx, 1, 1, 1, action)
		require.Error(t, err, action)
	}
	_, err = svc.AdminReview(ctx, 1, 1, 1, "approved", "valid")
	require.Error(t, err)
	cat.max = 3
	_, err = svc.Create(ctx, 1, "key", create)
	require.NoError(t, err)
	_, err = svc.Patch(ctx, 1, 1, SupplierAccountPatch{ExpectedVersion: 1})
	require.NoError(t, err)
	_, err = svc.Transition(ctx, 1, 1, 1, "submit")
	require.NoError(t, err)
	_, err = svc.Transition(ctx, 1, 1, 1, "resume")
	require.NoError(t, err)
	_, err = svc.AdminReview(ctx, 1, 1, 1, "approved", "valid")
	require.NoError(t, err)
	cat.err = errors.New("unpublished")
	_, err = svc.Create(ctx, 1, "key", create)
	require.Error(t, err)
}

func TestSupplierOAuthCreateRequiresPublishedPolicy(t *testing.T) {
	limit := 4
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, MaxSupplierConcurrency: &limit, Providers: []config.MarketplaceProviderConfig{{Platform: "openai", Label: "OpenAI", BaseURL: "https://api.openai.com/v1", AccountTypes: []string{"oauth"}, Models: []string{"m"}}}}}
	catalog := &policyAccountCatalog{err: errors.New("unpublished")}
	store := &policyAccountStore{}
	svc := NewSupplierAccountService(cfg, store, authorizationPolicyProfiles{profile: &SupplierProfile{Status: SupplierStatusActive}}, catalog, nil)
	require.Error(t, svc.AuthorizeSupplierAuthorization(context.Background(), 1, "create", 0))
	catalog.err = nil
	catalog.max = 1
	require.NoError(t, svc.AuthorizeSupplierAuthorization(context.Background(), 1, "create", 0))
}
