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

func TestMarketplaceGatewayReserveReplayAndVoid(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	var buyer, group, key int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, suffix+"@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,$3,'test') RETURNING id`, buyer, group, suffix).Scan(&key))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version,reconciled_at) VALUES($1,1,now())`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, buyer, suffix)
	require.NoError(t, err)
	repo := NewMarketplaceGatewayRepository(integrationDB, &config.Config{})
	idempotency := "same"
	cmd := service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "digest", IdempotencyKey: &idempotency}
	budget := service.Money{Decimal: decimal.NewFromInt(2)}
	receipt, created, err := repo.PrepareAndReserve(ctx, cmd, budget)
	require.NoError(t, err)
	require.True(t, created)
	again, created, err := repo.PrepareAndReserve(ctx, cmd, budget)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, receipt.ID, again.ID)
	var frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id=$1`, buyer).Scan(&frozen))
	require.True(t, frozen.Equal(budget.Decimal))
	require.NoError(t, repo.VoidUndispatched(ctx, receipt.ID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id=$1`, buyer).Scan(&frozen))
	require.True(t, frozen.IsZero())
}

func TestMarketplaceGatewayQualificationAndDispatchRecheck(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	var buyer, owner, group, key, account, offer int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, suffix+"-buyer@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"-owner@example.test").Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,$3,'test') RETURNING id`, buyer, group, suffix).Scan(&key))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id,migration_version,reconciled_at) VALUES($1,1,now())`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, buyer, suffix)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, owner)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, owner).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id,review_status,enabled,allowed_models) VALUES($1,$2,'approved',true,'["m"]') RETURNING id`, account, owner).Scan(&offer))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, account, group)
	require.NoError(t, err)
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, DispatchEnabled: true, Providers: []config.MarketplaceProviderConfig{{Platform: "openai", AccountTypes: []string{"apikey"}, Models: []string{"m"}}}}}
	repo := NewMarketplaceGatewayRepository(integrationDB, cfg)
	got, err := repo.Qualify(ctx, account, buyer, group, "m")
	require.NoError(t, err)
	require.Equal(t, offer, got)
	_, err = repo.Qualify(ctx, account, owner, group, "m")
	require.ErrorIs(t, err, service.ErrMarketplaceAccountIneligible)
	_, err = repo.Qualify(ctx, account, buyer, group, "other")
	require.ErrorIs(t, err, service.ErrMarketplaceAccountIneligible)
	receipt, _, err := repo.PrepareAndReserve(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "digest"}, service.Money{Decimal: decimal.NewFromInt(2)})
	require.NoError(t, err)
	rate := decimal.NewFromInt(1)
	price := service.Money{Decimal: rate}
	var version int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM marketplace_price_versions`).Scan(&version))
	book := service.MarketplacePriceBook{Groups: []service.MarketplaceCatalogGroup{{GroupID: group, Protocol: "responses", Models: []service.MarketplaceCatalogModel{{PublicModel: "m", ActualModel: "m", RateMultiplier: &rate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}}}}}}
	data, err := json.Marshal(book)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_policies(version,fee_bps,hold_hours,max_supplier_concurrency,minimum_payout_usd,published_by,published_at) VALUES($1,0,0,1,0,$2,now())`, version, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_price_versions(version,rules,published_by,published_at) VALUES($1,$2,$3,now())`, version, string(data), owner)
	require.NoError(t, err)
	q := service.QuoteSnapshot{Currency: "USD", PublicModel: "m", ActualModel: "m", Protocol: "responses", PriceVersion: version, PolicyVersion: version, RateMultiplier: &rate, AccountRateMultiplier: &rate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}, AccountID: account, OfferID: offer, OwnerUserID: owner, SnapshotAt: time.Now().UTC()}
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_profiles SET status='suspended' WHERE user_id=$1`, owner)
	require.NoError(t, err)
	_, err = repo.Dispatch(ctx, receipt.ID, q)
	require.ErrorIs(t, err, service.ErrMarketplaceAccountIneligible)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_profiles SET status='active' WHERE user_id=$1`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_payment_risk_flags(user_id,reason) VALUES($1,'review')`, buyer)
	require.NoError(t, err)
	_, err = repo.Dispatch(ctx, receipt.ID, q)
	require.ErrorIs(t, err, service.ErrMarketplaceRequestInvalid)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM marketplace_payment_risk_flags WHERE user_id=$1`, buyer)
	require.NoError(t, err)
	attempt, err := repo.Dispatch(ctx, receipt.ID, q)
	require.NoError(t, err)
	_, err = repo.Dispatch(ctx, receipt.ID, q)
	require.ErrorIs(t, err, service.ErrMarketplaceRequestState)
	require.ErrorIs(t, repo.VoidUndispatched(ctx, receipt.ID), service.ErrMarketplaceRequestState)
	require.NoError(t, repo.MarkReview(ctx, receipt.ID, attempt.ID, "unknown usage"))
	var frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id=$1`, buyer).Scan(&frozen))
	require.True(t, frozen.Equal(decimal.NewFromInt(2)))
	var admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, suffix+"-admin@example.test").Scan(&admin))
	require.ErrorIs(t, repo.Resolve(ctx, buyer, receipt.ID, "release_confirmed_unexecuted", "provider confirmed", "unauthorized"), service.ErrMarketplaceRequestInvalid)
	require.ErrorIs(t, repo.Resolve(ctx, admin, receipt.ID, "retry_known_usage", "retry", "unknown-usage"), service.ErrMarketplaceRequestState)
	require.NoError(t, repo.Resolve(ctx, admin, receipt.ID, "release_confirmed_unexecuted", "provider confirmed", "release-"+suffix))
	require.NoError(t, repo.Resolve(ctx, admin, receipt.ID, "release_confirmed_unexecuted", "provider confirmed", "release-"+suffix))
	require.ErrorIs(t, repo.Resolve(ctx, admin, receipt.ID, "release_confirmed_unexecuted", "different", "release-"+suffix), service.ErrMarketplaceIdempotencyConflict)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id=$1`, buyer).Scan(&frozen))
	require.True(t, frozen.IsZero())
	var audits int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_recovery_audits WHERE receipt_id=$1`, receipt.ID).Scan(&audits))
	require.Equal(t, 1, audits)

	// Persisted trusted usage may be retried but never released as unexecuted.
	receipt, _, err = repo.PrepareAndReserve(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "second"}, service.Money{Decimal: decimal.NewFromInt(2)})
	require.NoError(t, err)
	attempt, err = repo.Dispatch(ctx, receipt.ID, q)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewMarketplaceRequestRepository(integrationDB).RecordUsage(ctx, tx, receipt.ID, attempt.ID, service.UsageSnapshot{InputTokens: 1000000, Source: "upstream"}))
	require.NoError(t, tx.Commit())
	require.NoError(t, repo.MarkReview(ctx, receipt.ID, attempt.ID, "retry settlement"))
	require.ErrorIs(t, repo.Resolve(ctx, admin, receipt.ID, "release_confirmed_unexecuted", "incorrect", "invalid-release"), service.ErrMarketplaceRequestState)
	require.NoError(t, repo.Resolve(ctx, admin, receipt.ID, "retry_known_usage", "verified usage", "retry-"+suffix))
	require.NoError(t, repo.Resolve(ctx, admin, receipt.ID, "retry_known_usage", "verified usage", "retry-"+suffix))
	var state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, receipt.ID).Scan(&state))
	require.Equal(t, "settled", state)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_recovery_audits WHERE receipt_id=$1`, receipt.ID).Scan(&audits))
	require.Equal(t, 1, audits)

	result, err := repo.Complete(ctx, receipt.ID, attempt.ID, q, service.UsageSnapshot{InputTokens: 1000000, Source: "upstream"}, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.False(t, result.FirstApplied)
	result, err = repo.Complete(ctx, receipt.ID, attempt.ID, q, service.UsageSnapshot{InputTokens: 1000000, Source: "upstream"}, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.False(t, result.FirstApplied)
}
