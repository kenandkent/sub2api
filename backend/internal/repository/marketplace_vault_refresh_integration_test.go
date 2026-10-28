//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type vaultGatedRefresher struct{ entered, release chan struct{} }

func (r *vaultGatedRefresher) RefreshToken(ctx context.Context, token, proxy string) (*openai.TokenResponse, error) {
	close(r.entered)
	select {
	case <-r.release:
		return &openai.TokenResponse{AccessToken: "access-rotated", RefreshToken: "refresh-rotated", ExpiresIn: 3600}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type vaultRefreshLock struct{}

func (vaultRefreshLock) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (vaultRefreshLock) ReleaseRefreshLock(context.Context, string) error { return nil }
func (vaultRefreshLock) AcquireSupplierRefreshLock(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (vaultRefreshLock) ReleaseSupplierRefreshLock(context.Context, string, string) error { return nil }

func TestVaultRotationDuringOAuthRefreshPersistsRotatedToken(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var owner, admin, id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, uniqueTestValue(t, "vault-refresh-owner")+"@example.com").Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, uniqueTestValue(t, "vault-refresh-admin")+"@example.com").Scan(&admin))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(owner_user_id,name,platform,type,credentials) VALUES($1,'oauth','openai','oauth','{}') RETURNING id`, owner).Scan(&id))
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	old, err := service.NewCredentialEnvelope("old", map[string][]byte{"old": oldKey})
	require.NoError(t, err)
	current, err := service.NewCredentialEnvelope("new", map[string][]byte{"old": oldKey, "new": newKey})
	require.NoError(t, err)
	plain := []byte(`{"access_token":"old-access","refresh_token":"old-refresh","chatgpt_account_id":"acct","expires_at":"2020-01-01T00:00:00Z"}`)
	sealed, err := old.Seal("openai", owner, id, plain)
	require.NoError(t, err)
	raw, err := json.Marshal(sealed)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_build_object('_supplier_envelope',$1::jsonb) WHERE id=$2`, string(raw), id)
	require.NoError(t, err)
	gate := &vaultGatedRefresher{entered: make(chan struct{}), release: make(chan struct{})}
	account := &service.Account{ID: id, OwnerUserID: &owner, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	svc := service.NewSupplierRuntimeAccountService(current, NewSupplierRuntimeCredentialRepository(integrationDB), gate, vaultRefreshLock{}, service.SupplierRuntimeProviderConfig{})
	type result struct {
		account *service.Account
		err     error
	}
	results := make(chan result, 1)
	go func() { a, e := svc.Prepare(ctx, account); results <- result{a, e} }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	processed, left, done, err := NewSupplierPayoutRepository(integrationDB).RotateCredentialVault(ctx, admin, "refresh-rotation", "rotate key", 10, current)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	require.Zero(t, left)
	require.True(t, done)
	close(gate.release)
	got := <-results
	require.NoError(t, got.err, "ciphertext-only rotation must not discard the provider's refreshed token")
	require.Equal(t, "access-rotated", got.account.GetOpenAIAccessToken())
	stored, err := NewSupplierRuntimeCredentialRepository(integrationDB).LoadSupplierCredential(ctx, id, owner)
	require.NoError(t, err)
	require.Equal(t, "new", stored.KeyID)
	decoded, err := current.Open("openai", owner, id, stored)
	require.NoError(t, err)
	require.Contains(t, string(decoded), "refresh-rotated")
}

func TestOAuthRefreshNeverOverwritesReauthorizationOrDeletion(t *testing.T) {
	for _, action := range []string{"reauthorize", "delete"} {
		t.Run(action, func(t *testing.T) {
			isolateCommittedIntegrationTest(t)
			ctx := context.Background()
			var owner, id int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, uniqueTestValue(t, "vault-reauth")+"@example.com").Scan(&owner))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(owner_user_id,name,platform,type,credentials) VALUES($1,'oauth','openai','oauth','{}') RETURNING id`, owner).Scan(&id))
			env, err := service.NewCredentialEnvelope("key", map[string][]byte{"key": bytes.Repeat([]byte{2}, 32)})
			require.NoError(t, err)
			original, err := env.Seal("openai", owner, id, []byte(`{"access_token":"old","refresh_token":"old-refresh","chatgpt_account_id":"acct","expires_at":"2020-01-01T00:00:00Z"}`))
			require.NoError(t, err)
			raw, err := json.Marshal(original)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_build_object('_supplier_envelope',$1::jsonb) WHERE id=$2`, string(raw), id)
			require.NoError(t, err)
			gate := &vaultGatedRefresher{entered: make(chan struct{}), release: make(chan struct{})}
			account := &service.Account{ID: id, OwnerUserID: &owner, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
			svc := service.NewSupplierRuntimeAccountService(env, NewSupplierRuntimeCredentialRepository(integrationDB), gate, vaultRefreshLock{}, service.SupplierRuntimeProviderConfig{})
			results := make(chan error, 1)
			go func() { _, e := svc.Prepare(ctx, account); results <- e }()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("refresh did not start")
			}
			if action == "reauthorize" {
				newCredential, e := env.Seal("openai", owner, id, []byte(`{"access_token":"new-owner-token","refresh_token":"new-owner-refresh","chatgpt_account_id":"acct","expires_at":"2035-01-01T00:00:00Z"}`))
				require.NoError(t, e)
				next, e := json.Marshal(newCredential)
				require.NoError(t, e)
				_, e = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_build_object('_supplier_envelope',$1::jsonb) WHERE id=$2`, string(next), id)
				require.NoError(t, e)
			} else {
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=now() WHERE id=$1`, id)
				require.NoError(t, err)
			}
			close(gate.release)
			require.ErrorIs(t, <-results, service.ErrSupplierRuntimeUnavailable)
			if action == "reauthorize" {
				stored, e := NewSupplierRuntimeCredentialRepository(integrationDB).LoadSupplierCredential(ctx, id, owner)
				require.NoError(t, e)
				plain, e := env.Open("openai", owner, id, stored)
				require.NoError(t, e)
				require.Contains(t, string(plain), "new-owner-refresh")
				require.NotContains(t, string(plain), "refresh-rotated")
			}
		})
	}
}
