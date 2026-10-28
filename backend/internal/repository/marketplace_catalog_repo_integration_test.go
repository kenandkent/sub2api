//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceCatalogPublishedVersionsAndDedicatedGroups(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var actor, group int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'hash','admin') RETURNING id`, fmt.Sprintf("catalog-admin-%d@example.com", time.Now().UnixNano())).Scan(&actor))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id`, fmt.Sprintf("catalog-%d", actor)).Scan(&group))
	price := &service.Money{Decimal: decimal.RequireFromString("1")}
	rate := decimal.NewFromInt(1)
	book := service.MarketplacePriceBook{Groups: []service.MarketplaceCatalogGroup{{GroupID: group, Name: "Shared", Platform: "openai", Protocol: "responses", Models: []service.MarketplaceCatalogModel{{PublicModel: "shared", ActualModel: "upstream", ContextLimit: 16000, MaxOutputTokens: 4000, RateMultiplier: &rate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: price, OutputPerMillion: price, CacheReadPerMillion: price, CacheWritePerMillion: price}}}}}}}
	policy := service.MarketplacePolicyVersion{FeeBPS: 2000, HoldHours: 24, MaxSupplierConcurrency: 3, MinimumPayoutUSD: service.Money{Decimal: decimal.NewFromInt(1)}}
	repo := NewMarketplaceCatalogRepository(integrationDB)
	published, err := repo.Publish(ctx, actor, policy, book)
	require.NoError(t, err)
	require.Positive(t, published.Version)
	var notices int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM announcements WHERE title='Marketplace policy updated' AND content LIKE $1 AND status='active'`, fmt.Sprintf("%%version %d%%", published.Version)).Scan(&notices))
	require.Equal(t, 1, notices, "publication must create an in-app notice")
	current, err := repo.Current(ctx)
	require.NoError(t, err)
	require.Equal(t, published.Version, current.Version)
	require.Equal(t, published.Version, current.Policy.Version)
	shared, err := repo.IsSharedGroup(ctx, group)
	require.NoError(t, err)
	require.True(t, shared)
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_price_versions SET rules='{}' WHERE version=$1`, published.Version)
	require.Error(t, err)
	var account int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type) VALUES($1,'openai','api_key') RETURNING id`, fmt.Sprintf("self-%d", actor)).Scan(&account))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, account, group)
	require.NoError(t, err)
	_, err = repo.Publish(ctx, actor, policy, book)
	require.ErrorContains(t, err, "platform-owned")
}
