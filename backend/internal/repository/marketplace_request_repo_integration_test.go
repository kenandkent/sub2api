//go:build integration

package repository

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceRequestIdempotency(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	var user, key, group, account int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"@example.test").Scan(&user))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, user, suffix).Scan(&key))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `UPDATE marketplace_requests SET billable_attempt_id=NULL WHERE buyer_user_id=$1`, user)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM marketplace_attempts WHERE receipt_id IN (SELECT id FROM marketplace_requests WHERE buyer_user_id=$1)`, user)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM marketplace_requests WHERE buyer_user_id=$1`, user)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE user_id=$1`, user)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, user)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, group)
	})
	k := "same-key"
	command := service.MarketplacePrepareCommand{BuyerUserID: user, APIKeyID: key, GroupID: group, RequestDigest: "digest-a", IdempotencyKey: &k}
	const workers = 8
	receipts := make([]*service.MarketplaceReceipt, workers)
	created := make([]bool, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			receipts[i], created[i], errs[i] = NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, command)
		}(i)
	}
	wg.Wait()
	count := 0
	for i := range receipts {
		require.NoError(t, errs[i])
		require.Equal(t, receipts[0].ID, receipts[i].ID)
		if created[i] {
			count++
		}
	}
	require.Equal(t, 1, count)
	var otherKey int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, user, suffix+"-other").Scan(&otherKey))
	other := command
	other.APIKeyID = otherKey
	scoped, wasCreated, err := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, other)
	require.NoError(t, err)
	require.True(t, wasCreated)
	require.NotEqual(t, receipts[0].ID, scoped.ID)
	_, err = NewMarketplaceRequestRepository(integrationDB).GetOwned(ctx, receipts[0].ID, user, otherKey)
	require.ErrorIs(t, err, sql.ErrNoRows)
	command.RequestDigest = "digest-b"
	_, _, err = NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, command)
	require.ErrorIs(t, err, service.ErrMarketplaceIdempotencyConflict)
	command.IdempotencyKey = nil
	command.RequestDigest = "digest-a"
	first, _, err := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, command)
	require.NoError(t, err)
	second, _, err := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, command)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)

	// A failed zero-cost attempt may be retried, but an uncertain dispatch may not.
	repo := NewMarketplaceRequestRepository(integrationDB)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type) VALUES($1,'openai','apikey') RETURNING id`, suffix).Scan(&account))
	price := service.Money{Decimal: decimal.NewFromInt(1)}
	q := service.QuoteSnapshot{Currency: "USD", PublicModel: "model", ActualModel: "model", Protocol: "responses", PriceVersion: 1, PolicyVersion: 1, RateMultiplier: marketplaceRequestRate(), Tiers: []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}, AccountID: account, OfferID: 1, OwnerUserID: user, SnapshotAt: time.Now().UTC()}
	transact := func(fn func(*sql.Tx)) {
		tx, e := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, e)
		defer tx.Rollback()
		fn(tx)
		require.NoError(t, tx.Commit())
	}
	// A rolled-back hold transaction must not leave a reserved receipt.
	txRollback, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, repo.Reserve(ctx, txRollback, first.ID, price))
	require.NoError(t, txRollback.Rollback())
	rolledBack, err := repo.GetOwned(ctx, first.ID, user, key)
	require.NoError(t, err)
	require.Equal(t, "prepared", rolledBack.Status)
	transact(func(tx *sql.Tx) { require.NoError(t, repo.Reserve(ctx, tx, receipts[0].ID, price)) })
	var a *service.MarketplaceAttempt
	transact(func(tx *sql.Tx) {
		var e error
		a, e = repo.CreateAttempt(ctx, tx, receipts[0].ID, q)
		require.NoError(t, e)
	})

	// The persisted price and attribution snapshot cannot be rewritten.
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE marketplace_attempts SET quote='{}'::jsonb WHERE id=$1`, a.ID)
	require.Error(t, err)
	require.NoError(t, tx.Rollback())
	transact(func(tx *sql.Tx) { require.NoError(t, repo.MarkDispatched(ctx, tx, receipts[0].ID, a.ID)) })
	transact(func(tx *sql.Tx) {
		require.NoError(t, repo.ClassifyFailure(ctx, tx, receipts[0].ID, a.ID, service.MarketplaceRejectedBeforeExecution))
	})
	transact(func(tx *sql.Tx) {
		var e error
		a, e = repo.CreateAttempt(ctx, tx, receipts[0].ID, q)
		require.NoError(t, e)
		require.Equal(t, 2, a.Ordinal)
	})
	transact(func(tx *sql.Tx) { require.NoError(t, repo.MarkDispatched(ctx, tx, receipts[0].ID, a.ID)) })
	transact(func(tx *sql.Tx) {
		require.NoError(t, repo.ClassifyFailure(ctx, tx, receipts[0].ID, a.ID, "unknown"))
	})
	transact(func(tx *sql.Tx) {
		_, e := repo.CreateAttempt(ctx, tx, receipts[0].ID, q)
		require.ErrorIs(t, e, service.ErrMarketplaceRequestState)
	})
	usage := service.UsageSnapshot{InputTokens: 100, Source: "upstream"}
	transact(func(tx *sql.Tx) { require.NoError(t, repo.RecordUsage(ctx, tx, receipts[0].ID, a.ID, usage)) })
	charge := service.Money{Decimal: decimal.RequireFromString("0.0001")}
	transact(func(tx *sql.Tx) { require.NoError(t, repo.MarkSettled(ctx, tx, receipts[0].ID, a.ID, charge)) })
	command.IdempotencyKey = &k
	again, createdAgain, err := repo.Prepare(ctx, command)
	require.NoError(t, err)
	require.False(t, createdAgain)
	require.Equal(t, "settled", again.Status)
	require.Equal(t, receipts[0].ID, again.ID)

	// An underfunded actual charge stays in review until a funding top-up and
	// its receipt budget change commit together.
	small := service.Money{Decimal: decimal.RequireFromString("0.00001")}
	transact(func(tx *sql.Tx) { require.NoError(t, repo.Reserve(ctx, tx, first.ID, small)) })
	transact(func(tx *sql.Tx) { var e error; a, e = repo.CreateAttempt(ctx, tx, first.ID, q); require.NoError(t, e) })
	transact(func(tx *sql.Tx) { require.NoError(t, repo.MarkDispatched(ctx, tx, first.ID, a.ID)) })
	transact(func(tx *sql.Tx) { require.NoError(t, repo.RecordUsage(ctx, tx, first.ID, a.ID, usage)) })
	transact(func(tx *sql.Tx) {
		require.ErrorIs(t, repo.MarkSettled(ctx, tx, first.ID, a.ID, charge), service.ErrMarketplaceRequestInvalid)
	})
	transact(func(tx *sql.Tx) {
		require.NoError(t, repo.MarkNeedsReview(ctx, tx, first.ID, a.ID, "insufficient funding"))
	})
	transact(func(tx *sql.Tx) {
		require.NoError(t, repo.Reserve(ctx, tx, first.ID, price))
		require.NoError(t, repo.MarkSettled(ctx, tx, first.ID, a.ID, charge))
	})
}

func marketplaceRequestRate() *decimal.Decimal { d := decimal.NewFromInt(1); return &d }
