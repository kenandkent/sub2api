package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type supplierDeadlineStore struct {
	runtimeStoreStub
	loads                      int
	loadDeadline, saveDeadline time.Time
}

type supplierLateRefresher struct{}

func (supplierLateRefresher) RefreshToken(ctx context.Context, _, _ string) (*openai.TokenResponse, error) {
	<-ctx.Done()
	return &openai.TokenResponse{AccessToken: "late", RefreshToken: "late-refresh", ExpiresIn: 3600}, nil
}

func TestSupplierRefreshDoesNotPersistResponseAfterDeadline(t *testing.T) {
	env, account, store := runtimeTestAccount(t, map[string]any{"chatgpt_account_id": "acct", "access_token": "old", "refresh_token": "old-refresh", "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	svc := NewSupplierRuntimeAccountService(env, store, supplierLateRefresher{}, runtimeLockStub{}, SupplierRuntimeProviderConfig{})
	_, err := svc.Prepare(ctx, account)
	require.ErrorIs(t, err, ErrSupplierRuntimeUnavailable)
	require.Zero(t, store.writes)
}

func (s *supplierDeadlineStore) LoadSupplierCredential(ctx context.Context, _, _ int64) (EncryptedCredential, error) {
	s.loads++
	if s.loads > 1 {
		s.loadDeadline, _ = ctx.Deadline()
	}
	return s.current, nil
}
func (s *supplierDeadlineStore) CompareAndSwapSupplierCredential(ctx context.Context, account, owner int64, old, next EncryptedCredential) (bool, error) {
	s.saveDeadline, _ = ctx.Deadline()
	return s.runtimeStoreStub.CompareAndSwapSupplierCredential(ctx, account, owner, old, next)
}

func TestSupplierRefreshBoundsDatabaseLoadAndSave(t *testing.T) {
	env, account, initial := runtimeTestAccount(t, map[string]any{"chatgpt_account_id": "acct", "access_token": "old", "refresh_token": "old-refresh", "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	store := &supplierDeadlineStore{runtimeStoreStub: *initial}
	svc := NewSupplierRuntimeAccountService(env, store, &runtimeRefreshStub{}, runtimeLockStub{}, SupplierRuntimeProviderConfig{})
	_, err := svc.Prepare(context.Background(), account)
	require.NoError(t, err)
	require.False(t, store.loadDeadline.IsZero(), "credential reload must share the bounded lock lifetime")
	require.False(t, store.saveDeadline.IsZero(), "credential CAS must share the bounded lock lifetime")
	require.WithinDuration(t, store.loadDeadline, store.saveDeadline, time.Millisecond*10)
	require.Less(t, time.Until(store.saveDeadline), 30*time.Second)
}

type legacySupplierLock struct{}

func (legacySupplierLock) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (legacySupplierLock) ReleaseRefreshLock(context.Context, string) error { return nil }

func TestSupplierRefreshRejectsUnownedLockImplementation(t *testing.T) {
	env, account, store := runtimeTestAccount(t, map[string]any{"chatgpt_account_id": "acct", "access_token": "old", "refresh_token": "old-refresh", "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	refresher := &runtimeRefreshStub{}
	svc := NewSupplierRuntimeAccountService(env, store, refresher, legacySupplierLock{}, SupplierRuntimeProviderConfig{})
	_, err := svc.Prepare(context.Background(), account)
	require.ErrorIs(t, err, ErrSupplierRuntimeUnavailable)
	require.Zero(t, refresher.calls)
}
