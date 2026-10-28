//go:build integration

package repository

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierAccountRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	var owner, other int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'hash') RETURNING id`, uniqueTestValue(t, "supplier-account")+"@example.com").Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'hash') RETURNING id`, uniqueTestValue(t, "supplier-other")+"@example.com").Scan(&other))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active'),($2,'active')`, owner, other)
	require.NoError(t, err)
	env, err := service.NewCredentialEnvelope("test", map[string][]byte{"test": bytes.Repeat([]byte{9}, 32)})
	require.NoError(t, err)
	repo := NewSupplierAccountRepository(integrationDB, env)
	secret := "sk-0123456789-very-secret-credential"
	in := service.SupplierAccountCreate{Name: "Supplier", Platform: "openai", Type: "apikey", APIKey: secret, Concurrency: 2, AllowedModels: []string{"gpt-4o"}}
	provider := config.MarketplaceProviderConfig{BaseURL: "https://api.openai.com/v1"}
	op, err := repo.Create(ctx, owner, "create-1", "digest-1", in, provider)
	require.NoError(t, err)
	require.NotNil(t, op.AccountID)
	id := *op.AccountID
	var raw string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials::text FROM accounts WHERE id=$1`, id).Scan(&raw))
	require.False(t, strings.Contains(raw, secret))
	retry, err := repo.Create(ctx, owner, "create-1", "digest-1", in, provider)
	require.NoError(t, err)
	require.Equal(t, id, *retry.AccountID)
	_, err = repo.Create(ctx, owner, "create-1", "different", in, provider)
	require.Error(t, err)
	_, err = repo.Create(ctx, other, "create-2", "digest-2", in, provider)
	require.Error(t, err)
	_, err = repo.Get(ctx, other, id)
	require.Error(t, err)
	a, err := repo.Get(ctx, owner, id)
	require.NoError(t, err)
	require.Equal(t, int64(1), a.Offer.Version)
	_, err = repo.Patch(ctx, owner, id, service.SupplierAccountPatch{ExpectedVersion: 2})
	require.Error(t, err)
	name := "Renamed"
	a, err = repo.Patch(ctx, owner, id, service.SupplierAccountPatch{ExpectedVersion: 1, Name: &name})
	require.NoError(t, err)
	require.Equal(t, "Renamed", a.Name)
	require.Equal(t, int64(2), a.Offer.Version)
	a, err = repo.Transition(ctx, owner, id, 2, "submit")
	require.NoError(t, err)
	require.Equal(t, "pending_review", a.Offer.ReviewStatus)
	var group int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, uniqueTestValue(t, "supplier-group")).Scan(&group))
	_, err = repo.Review(ctx, other, a.Offer.ID, 2, "approved", "reviewed", []int64{group})
	require.Error(t, err)
	a, err = repo.Review(ctx, other, a.Offer.ID, 3, "approved", "reviewed", []int64{group})
	require.NoError(t, err)
	require.Equal(t, "approved", a.Offer.ReviewStatus)
	require.True(t, a.Offer.Enabled)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_groups WHERE account_id=$1 AND group_id=$2`, id, group).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sharing_offer_review_audits WHERE offer_id=$1`, a.Offer.ID).Scan(&count))
	require.Equal(t, 1, count)
	rotated, err := repo.ReplaceCredentials(ctx, owner, id, "rotate-1", "rotate-digest", "sk-another-strong-secret-credential", a.Offer.Version)
	require.NoError(t, err)
	require.Equal(t, id, *rotated.AccountID)
	a, err = repo.Get(ctx, owner, id)
	require.NoError(t, err)
	require.Equal(t, "pending_review", a.Offer.ReviewStatus)
	require.False(t, a.Offer.Enabled)
	retryRotate, err := repo.ReplaceCredentials(ctx, owner, id, "rotate-1", "rotate-digest", "sk-another-strong-secret-credential", 4)
	require.NoError(t, err)
	require.Equal(t, id, *retryRotate.AccountID)
	probeKey, err := repo.ReserveProbe(ctx, owner, id)
	require.NoError(t, err)
	require.Equal(t, "sk-another-strong-secret-credential", probeKey)
	_, err = repo.ReserveProbe(ctx, owner, id)
	require.Error(t, err)
	_, err = repo.ReserveProbe(ctx, other, id)
	require.Error(t, err)
	draft := in
	draft.APIKey = ""
	draft.Name = "Draft"
	draftOp, err := repo.Create(ctx, owner, "draft-1", "draft-digest", draft, provider)
	require.NoError(t, err)
	draftAccount, err := repo.Get(ctx, owner, *draftOp.AccountID)
	require.NoError(t, err)
	require.Equal(t, "pending", draftAccount.CredentialStatus)

}
