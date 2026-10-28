//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNonSupplierCannotCreateOrVerifyDestination(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var user, admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES ('review-nonsupplier@example.test','x') RETURNING id`).Scan(&user))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES ('review-admin@example.test','x','admin') RETURNING id`).Scan(&admin))
	var profiles int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM supplier_profiles WHERE user_id=$1`, user).Scan(&profiles))
	require.Zero(t, profiles)
	env, err := service.NewCredentialEnvelope("test", map[string][]byte{"test": make([]byte, 32)})
	require.NoError(t, err)
	svc := service.NewSupplierPayoutService(NewSupplierPayoutRepository(integrationDB), nil, env)
	_, err = svc.CreateDestination(ctx, user, "bank ending 1234", []byte("test-bank-data"), "review-create")
	require.Error(t, err, "a user without a supplier profile cannot create a payout destination")
	var destination int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO supplier_payout_destinations(supplier_id,masked_label,credential,version) VALUES($1,'ending 1234','{}',1) RETURNING id`, user).Scan(&destination))
	_, err = svc.VerifyDestination(ctx, destination, admin, "review-verify", "test verification")
	require.Error(t, err, "an orphan destination must not be verified")
}
