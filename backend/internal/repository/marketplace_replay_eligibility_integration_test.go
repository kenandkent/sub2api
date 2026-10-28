//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceReplaySurvivesQuotaAndDebtWithoutNewHold(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	code := uuid.NewString()
	var buyer, key, group int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, code+"@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO groups(name) VALUES($1) RETURNING id`, code).Scan(&group))
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,$3,'test') RETURNING id`, buyer, group, code).Scan(&key))
	_, err := integrationDB.Exec(`INSERT INTO wallet_funding_states(user_id,migration_version,reconciled_at) VALUES($1,1,now())`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, buyer, code)
	require.NoError(t, err)
	repo := NewMarketplaceGatewayRepository(integrationDB, &config.Config{})
	idempotency := "same"
	cmd := service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "digest", IdempotencyKey: &idempotency}
	budget := service.Money{Decimal: decimal.NewFromInt(2)}
	first, created, err := repo.PrepareAndReserve(ctx, cmd, budget)
	require.NoError(t, err)
	require.True(t, created)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().SpendSelfOwned(ctx, tx, buyer, code+"-spent", "test", decimal.NewFromInt(4)))
	require.NoError(t, tx.Commit())
	_, err = integrationDB.Exec(`UPDATE api_keys SET status='quota_exhausted',quota=1,quota_used=1 WHERE id=$1`, key)
	require.NoError(t, err)
	got, created, err := repo.PrepareAndReserve(ctx, cmd, budget)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.ID, got.ID)
	lookup, ok := any(repo).(interface {
		LookupReplay(context.Context, service.MarketplacePrepareCommand) (*service.MarketplaceReceipt, error)
	})
	require.True(t, ok)
	got, err = lookup.LookupReplay(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, first.ID, got.ID)
	conflict := cmd
	conflict.RequestDigest = "different"
	_, err = lookup.LookupReplay(ctx, conflict)
	require.ErrorIs(t, err, service.ErrMarketplaceIdempotencyConflict)
	exists, err := repo.MarketplaceReplayExists(ctx, buyer, key, group, idempotency)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = repo.MarketplaceReplayExists(ctx, buyer+1, key, group, idempotency)
	require.NoError(t, err)
	require.False(t, exists)
	other := cmd
	other.BuyerUserID++
	got, err = lookup.LookupReplay(ctx, other)
	require.NoError(t, err)
	require.Nil(t, got)
	_, err = integrationDB.Exec(`UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE id=$1`, key)
	require.NoError(t, err)
	exists, err = repo.MarketplaceReplayExists(ctx, buyer, key, group, idempotency)
	require.NoError(t, err)
	require.False(t, exists)
	got, err = lookup.LookupReplay(ctx, cmd)
	require.NoError(t, err)
	require.Nil(t, got)
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM marketplace_requests WHERE buyer_user_id=$1`, buyer).Scan(&count))
	require.Equal(t, 1, count)
}
