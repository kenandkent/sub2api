//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// The publication advisory lock serializes dispatches before either takes a
// buyer funding lock. Reciprocal buyers must not deadlock on owner status.
func TestMarketplaceGatewayReciprocalDispatch(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var users, keys, accounts, offers [2]int64
	var group int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, uuid.NewString()).Scan(&group))
	for i := range users {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, uuid.NewString()+"@example.test").Scan(&users[i]))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,$3,'test') RETURNING id`, users[i], group, uuid.NewString()).Scan(&keys[i]))
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version,reconciled_at) VALUES($1,1,now())`, users[i])
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, users[i], uuid.NewString())
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, users[i])
		require.NoError(t, err)
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id,concurrency) VALUES($1,'openai','apikey',$2,1) RETURNING id`, uuid.NewString(), users[i]).Scan(&accounts[i]))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id,review_status,enabled,allowed_models) VALUES($1,$2,'approved',true,'["m"]') RETURNING id`, accounts[i], users[i]).Scan(&offers[i]))
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, accounts[i], group)
		require.NoError(t, err)
	}
	rate := decimal.NewFromInt(1)
	price := service.Money{Decimal: rate}
	tiers := []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}
	book := service.MarketplacePriceBook{Groups: []service.MarketplaceCatalogGroup{{GroupID: group, Protocol: "responses", Models: []service.MarketplaceCatalogModel{{PublicModel: "m", ActualModel: "m", RateMultiplier: &rate, Tiers: tiers}}}}}
	data, err := json.Marshal(book)
	require.NoError(t, err)
	var version int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM marketplace_price_versions`).Scan(&version))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_policies(version,fee_bps,hold_hours,max_supplier_concurrency,minimum_payout_usd,published_by,published_at) VALUES($1,0,0,1,0,$2,now())`, version, users[0])
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_price_versions(version,rules,published_by,published_at) VALUES($1,$2,$3,now())`, version, string(data), users[0])
	require.NoError(t, err)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, DispatchEnabled: true, Providers: []config.MarketplaceProviderConfig{{Platform: "openai", AccountTypes: []string{"apikey"}, Models: []string{"m"}}}}}
	repo := NewMarketplaceGatewayRepository(integrationDB, cfg)
	var receipts [2]*service.MarketplaceReceipt
	var quotes [2]service.QuoteSnapshot
	for i := range users {
		receipts[i], _, err = repo.PrepareAndReserve(ctx, service.MarketplacePrepareCommand{BuyerUserID: users[i], APIKeyID: keys[i], GroupID: group, RequestDigest: uuid.NewString()}, service.Money{Decimal: rate})
		require.NoError(t, err)
		j := 1 - i
		quotes[i] = service.QuoteSnapshot{Currency: "USD", PublicModel: "m", ActualModel: "m", Protocol: "responses", PriceVersion: version, PolicyVersion: version, RateMultiplier: &rate, PublishedRateMultiplier: &rate, ConsumerRateMultiplier: &rate, AccountRateMultiplier: &rate, Tiers: tiers, AccountID: accounts[j], OfferID: offers[j], OwnerUserID: users[j], SnapshotAt: time.Now().UTC()}
	}
	blocker, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	rows, err := blocker.QueryContext(ctx, `SELECT id FROM accounts WHERE id IN ($1,$2) ORDER BY id FOR UPDATE`, accounts[0], accounts[1])
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	results := make(chan error, 2)
	for i := range users {
		go func(i int) { _, err := repo.Dispatch(ctx, receipts[i].ID, quotes[i]); results <- err }(i)
	}
	// One dispatch waits for the held account; the other waits on the
	// transaction-scoped publication lock. Both must have reached PostgreSQL.
	waiting := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		err = integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND (query LIKE 'SELECT pg_advisory_xact_lock%' OR query LIKE 'SELECT o.id,o.owner_user_id,a.platform%')`).Scan(&count)
		if err != nil {
			break
		}
		if count >= 2 {
			waiting = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, blocker.Rollback())
	require.NoError(t, err)
	require.True(t, waiting, "both dispatches must be blocked before releasing accounts")
	require.NoError(t, <-results)
	require.NoError(t, <-results)
}
