package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type runtimeStoreStub struct {
	current EncryptedCredential
	writes  int
}

func (s *runtimeStoreStub) LoadSupplierCredential(context.Context, int64, int64) (EncryptedCredential, error) {
	return s.current, nil
}
func (s *runtimeStoreStub) CompareAndSwapSupplierCredential(_ context.Context, _, _ int64, old, replacement EncryptedCredential) (bool, error) {
	if !bytes.Equal(old.Ciphertext, s.current.Ciphertext) {
		return false, nil
	}
	s.current = replacement
	s.writes++
	return true, nil
}

type runtimeRefreshStub struct {
	calls   int
	idToken string
}

func (s *runtimeRefreshStub) RefreshToken(_ context.Context, refresh, proxy string) (*openai.TokenResponse, error) {
	s.calls++
	return &openai.TokenResponse{AccessToken: "new-token", RefreshToken: "new-refresh", ExpiresIn: 3600, IDToken: s.idToken}, nil
}

type runtimeLockStub struct{}

func (runtimeLockStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (runtimeLockStub) ReleaseRefreshLock(context.Context, string) error { return nil }

func runtimeTestAccount(t *testing.T, creds map[string]any) (*CredentialEnvelope, *Account, *runtimeStoreStub) {
	t.Helper()
	env, err := NewCredentialEnvelope("key", map[string][]byte{"key": bytes.Repeat([]byte{1}, 32)})
	require.NoError(t, err)
	raw, err := json.Marshal(creds)
	require.NoError(t, err)
	sealed, err := env.Seal("openai", 7, 8, raw)
	require.NoError(t, err)
	owner := int64(7)
	return env, &Account{ID: 8, OwnerUserID: &owner, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"_supplier_envelope": sealed, "model_mapping": map[string]any{"public": "actual"}}}, &runtimeStoreStub{current: sealed}
}
func TestMarketplaceSupplierRuntimePreparesEphemeralAccount(t *testing.T) {
	env, a, store := runtimeTestAccount(t, map[string]any{"chatgpt_account_id": "account-a", "access_token": "secret", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	proxyID := int64(9)
	a.ProxyID = &proxyID
	a.Proxy = &Proxy{ID: proxyID}
	svc := NewSupplierRuntimeAccountService(env, store, nil, nil, SupplierRuntimeProviderConfig{})
	out, err := svc.Prepare(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, "secret", out.GetOpenAIAccessToken())
	require.Equal(t, a.ProxyID, out.ProxyID)
	require.Same(t, a.Proxy, out.Proxy)
	require.Contains(t, a.Credentials, "_supplier_envelope")
	require.Empty(t, a.GetOpenAIAccessToken())
	require.NotSame(t, a, out)
	require.Equal(t, 0, store.writes)
}
func TestMarketplaceSupplierRuntimeRefreshPersistsOnlyEnvelope(t *testing.T) {
	env, a, store := runtimeTestAccount(t, map[string]any{"chatgpt_account_id": "account-a", "access_token": "old", "refresh_token": "refresh", "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	refresh := &runtimeRefreshStub{}
	svc := NewSupplierRuntimeAccountService(env, store, refresh, runtimeLockStub{}, SupplierRuntimeProviderConfig{})
	out, err := svc.Prepare(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, "new-token", out.GetOpenAIAccessToken())
	require.Equal(t, 1, refresh.calls)
	require.Equal(t, 1, store.writes)
	raw, err := env.Open("openai", 7, 8, store.current)
	require.NoError(t, err)
	require.Contains(t, string(raw), "new-token")
	require.Empty(t, a.GetOpenAIAccessToken())
}
func TestMarketplaceSupplierRuntimeRejectsPlaintextFallback(t *testing.T) {
	env, a, store := runtimeTestAccount(t, map[string]any{"access_token": "secret"})
	a.Credentials = map[string]any{"access_token": "plaintext"}
	store.current = EncryptedCredential{}
	svc := NewSupplierRuntimeAccountService(env, store, nil, nil, SupplierRuntimeProviderConfig{})
	_, err := svc.Prepare(context.Background(), a)
	require.Error(t, err)
}

func TestMarketplaceSupplierRuntimeRequiresEncryptedIdentity(t *testing.T) {
	env, a, store := runtimeTestAccount(t, map[string]any{"access_token": "secret", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	a.Credentials["chatgpt_account_id"] = "untrusted"
	_, err := NewSupplierRuntimeAccountService(env, store, nil, nil, SupplierRuntimeProviderConfig{}).Prepare(context.Background(), a)
	require.Error(t, err)
}
func TestMarketplaceSupplierRuntimeRefreshRejectsChangedIdentity(t *testing.T) {
	env, a, store := runtimeTestAccount(t, map[string]any{"chatgpt_account_id": "account-a", "access_token": "old", "refresh_token": "refresh", "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"account-b"}}`))
	refresh := &runtimeRefreshStub{idToken: "e30." + payload + ".signature"}
	_, err := NewSupplierRuntimeAccountService(env, store, refresh, runtimeLockStub{}, SupplierRuntimeProviderConfig{}).Prepare(context.Background(), a)
	require.Error(t, err)
	require.Zero(t, store.writes)
}
