//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type supplierOwnedRefreshLock interface {
	AcquireSupplierRefreshLock(context.Context, string, string, time.Duration) (bool, error)
	ReleaseSupplierRefreshLock(context.Context, string, string) error
}

type supplierBlockingCAS struct{ enc service.EncryptedCredential }

func (s *supplierBlockingCAS) LoadSupplierCredential(context.Context, int64, int64) (service.EncryptedCredential, error) {
	return s.enc, nil
}
func (s *supplierBlockingCAS) CompareAndSwapSupplierCredential(ctx context.Context, _, _ int64, _, _ service.EncryptedCredential) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

type supplierImmediateRefresh struct{}

func (supplierImmediateRefresh) RefreshToken(context.Context, string, string) (*openai.TokenResponse, error) {
	return &openai.TokenResponse{AccessToken: "new", RefreshToken: "new-refresh", ExpiresIn: 3600}, nil
}

func TestSupplierRefreshBoundIncludesBlockedSaveWithRealRedis(t *testing.T) {
	owner, id := int64(7), time.Now().UnixNano()
	env, err := service.NewCredentialEnvelope("key", map[string][]byte{"key": bytes.Repeat([]byte{3}, 32)})
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]string{"chatgpt_account_id": "acct", "access_token": "old", "refresh_token": "old-refresh", "expires_at": "2020-01-01T00:00:00Z"})
	require.NoError(t, err)
	enc, err := env.Seal("openai", owner, id, raw)
	require.NoError(t, err)
	cache := NewGeminiTokenCache(integrationRedis)
	svc := service.NewSupplierRuntimeAccountService(env, &supplierBlockingCAS{enc: enc}, supplierImmediateRefresh{}, cache, service.SupplierRuntimeProviderConfig{})
	account := &service.Account{ID: id, OwnerUserID: &owner, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = svc.Prepare(ctx, account)
	require.ErrorIs(t, err, service.ErrSupplierRuntimeUnavailable)
	key := fmt.Sprintf("%ssupplier-runtime:%d", oauthRefreshLockKeyPrefix, id)
	require.Zero(t, integrationRedis.Exists(context.Background(), key).Val(), "the same lock owner releases after timeout")
}

func TestSupplierRefreshLockStaleOwnerCannotReleaseNewOwner(t *testing.T) {
	ctx := context.Background()
	lock, ok := any(NewGeminiTokenCache(integrationRedis)).(supplierOwnedRefreshLock)
	require.True(t, ok, "supplier refresh requires owner-safe lock operations")
	key := uniqueTestValue(t, "supplier-lock")
	t.Cleanup(func() { _ = integrationRedis.Del(context.Background(), oauthRefreshLockKeyPrefix+key).Err() })
	acquired, err := lock.AcquireSupplierRefreshLock(ctx, key, "owner-a", 100*time.Millisecond)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Eventually(t, func() bool { return integrationRedis.Exists(ctx, oauthRefreshLockKeyPrefix+key).Val() == 0 }, time.Second, 10*time.Millisecond)
	acquired, err = lock.AcquireSupplierRefreshLock(ctx, key, "owner-b", 5*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, lock.ReleaseSupplierRefreshLock(ctx, key, "owner-a"))
	acquired, err = lock.AcquireSupplierRefreshLock(ctx, key, "owner-c", time.Second)
	require.NoError(t, err)
	require.False(t, acquired, "stale owner cannot delete the current owner's lock")
	require.NoError(t, lock.ReleaseSupplierRefreshLock(ctx, key, "owner-b"))
	acquired, err = lock.AcquireSupplierRefreshLock(ctx, key, "owner-c", time.Second)
	require.NoError(t, err)
	require.True(t, acquired)
}
