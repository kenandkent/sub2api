//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestReviewMarketplaceReserveInvalidatesLegacyAdmission(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	user, code := reviewUser(t, 0)
	reviewMigrate(t, user)
	order := reviewCreateOrder(t, user, "COMPLETED", 0, code)
	_, err := integrationDB.ExecContext(ctx, `UPDATE payment_orders SET bonus_amount=0 WHERE id=$1`, order)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SELECT set_config('sub2api.payment_redeem_code',$1,true)`, code)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+100 WHERE id=$1`, user)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var key, group int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, code).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, user, code).Scan(&key))
	receipt, _, err := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: user, APIKeyID: key, GroupID: group, RequestDigest: "digest"})
	require.NoError(t, err)
	cache := NewBillingCache(integrationRedis)
	require.NoError(t, cache.SetUserBalance(ctx, user, 100))
	svc := service.NewBillingCacheService(cache, newUserRepositoryWithSQL(integrationEntClient, integrationDB), nil, nil, nil, nil, &config.Config{}, nil)
	defer svc.Stop()
	subject := &service.User{ID: user}
	require.NoError(t, svc.CheckBillingEligibility(ctx, subject, nil, nil, nil, ""))
	require.NoError(t, NewMarketplaceBillingRepository(integrationDB).ReserveBudget(ctx, receipt.ID, user, service.Money{Decimal: decimal.NewFromInt(100)}))
	var bal decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user).Scan(&bal))
	require.True(t, bal.IsZero())
	staleErr := svc.CheckBillingEligibility(ctx, subject, nil, nil, nil, "")
	require.NoError(t, cache.InvalidateUserBalance(ctx, user))
	freshErr := svc.CheckBillingEligibility(ctx, subject, nil, nil, nil, "")
	t.Logf("after hold balance=%s, stale admission error=%v, fresh admission error=%v", bal, staleErr, freshErr)
	require.Error(t, freshErr, "fresh check must reject zero balance")
	if staleErr == nil {
		t.Errorf("stale cache admitted new legacy request after full marketplace hold")
	}
}

func TestFundingBalanceInvalidationIsDurableAndRetries(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	user, code := reviewUser(t, 10)
	reviewMigrate(t, user)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().SpendSelfOwned(ctx, tx, user, code+"-rollback", "test", decimal.NewFromInt(1)))
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM marketplace_balance_invalidation_tasks WHERE user_id=$1`, user).Scan(&count))
	require.Zero(t, count)
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().SpendSelfOwned(ctx, tx, user, code+"-commit", "test", decimal.NewFromInt(1)))
	require.NoError(t, tx.Commit())
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM marketplace_balance_invalidation_tasks WHERE user_id=$1 AND processed_at IS NULL`, user).Scan(&count))
	require.Equal(t, 1, count)
	fake := &marketplaceProjectionCacheFake{fail: true}
	worker := NewMarketplaceBalanceInvalidationWorker(integrationDB, fake)
	done, err := worker.ProcessOne(ctx)
	require.True(t, done)
	require.Error(t, err)
	_, err = integrationDB.Exec(`UPDATE marketplace_balance_invalidation_tasks SET available_at=now() WHERE user_id=$1`, user)
	require.NoError(t, err)
	fake.fail = false
	done, err = worker.ProcessOne(ctx)
	require.True(t, done)
	require.NoError(t, err)
	require.Equal(t, user, fake.user)
	_, err = integrationDB.Exec(`UPDATE marketplace_balance_invalidation_tasks SET available_at=now() WHERE user_id=$1`, user)
	require.NoError(t, err)
	done, err = worker.ProcessOne(ctx)
	require.True(t, done)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM marketplace_balance_invalidation_tasks WHERE user_id=$1 AND processed_at IS NULL`, user).Scan(&count))
	require.Zero(t, count)
	// The database compatibility bridge must enqueue changes made outside the
	// explicit wallet repository as well (e.g. legacy balance operations).
	_, err = integrationDB.Exec(`UPDATE users SET balance=balance-1 WHERE id=$1`, user)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM marketplace_balance_invalidation_tasks WHERE user_id=$1 AND processed_at IS NULL`, user).Scan(&count))
	require.Equal(t, 1, count)
}
