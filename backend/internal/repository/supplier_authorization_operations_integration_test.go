//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierAuthorizationOperationAtomicClaim(t *testing.T) {
	ctx := context.Background()
	// The temporary table mirrors migration 257 when it has not yet been merged.
	integrationDB.SetMaxOpenConns(1)
	defer integrationDB.SetMaxOpenConns(0)
	_, err := integrationDB.ExecContext(ctx, `CREATE TEMP TABLE supplier_operations (id bigserial primary key, user_id bigint not null, operation_key text not null, kind text not null, request_digest text not null, status text not null, account_id bigint, metadata jsonb not null default '{}', error_code text not null default '', created_at timestamptz not null default now(), updated_at timestamptz not null default now(), unique(user_id,operation_key))`)
	require.NoError(t, err)
	defer integrationDB.ExecContext(ctx, `DROP TABLE IF EXISTS pg_temp.supplier_operations`)
	store := NewSupplierAuthorizationOperationStore(integrationDB)
	require.NoError(t, store.Create(ctx, service.SupplierAuthorizationOperation{UserID: 7, ID: "session", Purpose: "create", Provider: "openai", StateDigest: "state-hash"}))
	missing, err := store.Get(ctx, 8, "session")
	require.NoError(t, err)
	require.Nil(t, missing)
	var wg sync.WaitGroup
	claims := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := store.Claim(ctx, 7, "session", "callback-hash")
			claims <- ok
			errs <- e
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	count := 0
	for ok := range claims {
		if ok {
			count++
		}
	}
	require.Equal(t, 1, count)
	op, err := store.Get(ctx, 7, "session")
	require.NoError(t, err)
	require.Equal(t, "processing", op.Status)
	require.Equal(t, "callback-hash", op.CallbackDigest)
	require.Equal(t, "state-hash", op.StateDigest)
	require.NoError(t, store.Fail(ctx, 7, "session", "processing", "write_uncertain"))
	op, err = store.Get(ctx, 7, "session")
	require.NoError(t, err)
	require.Equal(t, "needs_review", op.Status)
}
