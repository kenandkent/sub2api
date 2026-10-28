package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type runtimeRepeatedRotationStore struct {
	env      *CredentialEnvelope
	current  EncryptedCredential
	attempts int
}

func (s *runtimeRepeatedRotationStore) LoadSupplierCredential(context.Context, int64, int64) (EncryptedCredential, error) {
	return s.current, nil
}
func (s *runtimeRepeatedRotationStore) CompareAndSwapSupplierCredential(_ context.Context, account, owner int64, _, _ EncryptedCredential) (bool, error) {
	s.attempts++
	plain, err := s.env.Open("openai", owner, account, s.current)
	if err != nil {
		return false, err
	}
	s.current, err = s.env.Seal("openai", owner, account, plain)
	for i := range plain {
		plain[i] = 0
	}
	return false, err
}

func TestSupplierRefreshRotationRetryIsBounded(t *testing.T) {
	env, account, initial := runtimeTestAccount(t, map[string]any{"chatgpt_account_id": "account-a", "access_token": "old", "refresh_token": "refresh", "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	store := &runtimeRepeatedRotationStore{env: env, current: initial.current}
	svc := NewSupplierRuntimeAccountService(env, store, &runtimeRefreshStub{}, runtimeLockStub{}, SupplierRuntimeProviderConfig{})
	_, err := svc.Prepare(context.Background(), account)
	require.ErrorIs(t, err, ErrSupplierRuntimeUnavailable)
	require.Equal(t, 4, store.attempts, "initial CAS plus at most three retries")
}
