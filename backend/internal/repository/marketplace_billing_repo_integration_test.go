//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceBillingReserveInsufficientIsAtomic(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	var user, key, group int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',1) RETURNING id`, suffix+"@example.test").Scan(&user))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, user)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,1,1)`, user, suffix)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, user, suffix).Scan(&key))
	receipt, _, err := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: user, APIKeyID: key, GroupID: group, RequestDigest: "digest"})
	require.NoError(t, err)
	repo := NewMarketplaceBillingRepository(integrationDB)
	err = repo.ReserveBudget(ctx, receipt.ID, user, service.Money{Decimal: decimal.NewFromInt(2)})
	require.ErrorIs(t, err, service.ErrInsufficientFunding)
	var balance, frozen, budget string
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance::text,u.frozen_balance::text,r.budget::text,r.status FROM users u JOIN marketplace_requests r ON r.buyer_user_id=u.id WHERE r.id=$1`, receipt.ID).Scan(&balance, &frozen, &budget, &status))
	require.Equal(t, "1.00000000", balance)
	require.Equal(t, "0.00000000", frozen)
	require.Equal(t, "0.00000000", budget)
	require.Equal(t, "prepared", status)
}

func TestMarketplaceBillingCaptureAtomicReplayAndConservation(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	var buyer, owner, key, group, account, offer int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, suffix+"-buyer@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"-owner@example.test").Scan(&owner))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, buyer, suffix)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, buyer, suffix).Scan(&key))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, owner).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, account, owner).Scan(&offer))
	requests := NewMarketplaceRequestRepository(integrationDB)
	receipt, _, err := requests.Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "digest"})
	require.NoError(t, err)
	billing := NewMarketplaceBillingRepository(integrationDB)
	require.NoError(t, billing.ReserveBudget(ctx, receipt.ID, buyer, service.Money{Decimal: decimal.NewFromInt(2)}))
	price := service.Money{Decimal: decimal.NewFromInt(1)}
	rate := decimal.NewFromInt(1)
	accountRate := decimal.NewFromInt(2)
	q := service.QuoteSnapshot{Currency: "USD", PublicModel: "m", ActualModel: "m", Protocol: "responses", PriceVersion: 1, PolicyVersion: 1, RateMultiplier: &rate, AccountRateMultiplier: &accountRate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}, FeeBPS: 2000, HoldHours: 24, AccountID: account, OfferID: offer, OwnerUserID: owner, SnapshotAt: time.Now().UTC()}
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	attempt, err := requests.CreateAttempt(ctx, tx, receipt.ID, q)
	require.NoError(t, err)
	require.NoError(t, requests.MarkDispatched(ctx, tx, receipt.ID, attempt.ID))
	require.NoError(t, tx.Commit())
	digest, err := service.MarketplaceQuoteDigest(q)
	require.NoError(t, err)
	usage := service.UsageSnapshot{InputTokens: 1234567, Source: "upstream"}
	charge, err := service.NewMarketplaceQuoteService().CalculateActual(q, usage)
	require.NoError(t, err)
	net, fee, err := service.SplitMarketplaceCharge(charge, 2000)
	require.NoError(t, err)
	command := service.MarketplaceCaptureCommand{ReceiptID: receipt.ID, BillableAttemptID: attempt.ID, QuoteDigest: digest, Usage: usage, ConsumerCharge: charge, SupplierNet: net, PlatformFee: fee}
	firstCommand := command
	// Crossing quota must be returned again on a replay after an unknown commit result.
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET quota=1 WHERE id=$1`, key)
	require.NoError(t, err)
	// An overflow in a quota update fails the transaction after the dedup claim.
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET quota_used=999999999999.99999999 WHERE id=$1`, key)
	require.NoError(t, err)
	_, err = billing.Capture(ctx, command, service.UsageBillingCommand{})
	require.Error(t, err)
	var status string
	var captures, dedup int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, receipt.ID).Scan(&status))
	require.Equal(t, "dispatched", status)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_captures WHERE receipt_id=$1`, receipt.ID).Scan(&captures))
	require.Zero(t, captures)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, "marketplace:"+receipt.ID).Scan(&dedup))
	require.Zero(t, dedup)
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET quota_used=0 WHERE id=$1`, key)
	require.NoError(t, err)
	result, err := billing.Capture(ctx, command, service.UsageBillingCommand{BalanceCost: 999})
	require.NoError(t, err)
	require.True(t, result.FirstApplied)
	require.True(t, result.APIKeyQuotaExhausted)
	require.True(t, result.APIKeyRateLimitChanged)
	require.NotNil(t, result.NewBalance)
	require.Equal(t, 3.765433, *result.NewBalance)
	replay, err := billing.Capture(ctx, command, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.False(t, replay.FirstApplied)
	require.Equal(t, result.LedgerTransactionID, replay.LedgerTransactionID)
	require.True(t, replay.APIKeyQuotaExhausted)
	require.True(t, replay.APIKeyRateLimitChanged)
	require.Equal(t, result.NewBalance, replay.NewBalance)
	var persistedExhausted, persistedRate bool
	var persistedBalance string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT api_key_quota_exhausted,api_key_rate_limit_changed,buyer_balance_after::text FROM marketplace_captures WHERE receipt_id=$1`, receipt.ID).Scan(&persistedExhausted, &persistedRate, &persistedBalance))
	require.True(t, persistedExhausted)
	require.True(t, persistedRate)
	require.Equal(t, "3.76543300", persistedBalance)
	command.Usage.InputTokens++
	_, err = billing.Capture(ctx, command, service.UsageBillingCommand{})
	require.Error(t, err)
	var balance, frozen, pending, quota, c, n, f string
	var outbox int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.balance::text,u.frozen_balance::text,w.pending::text,k.quota_used::text,c.consumer_charge::text,c.supplier_net::text,c.platform_fee::text FROM users u JOIN api_keys k ON k.id=$2 JOIN supplier_wallets w ON w.user_id=$3 JOIN marketplace_captures c ON c.receipt_id=$4 WHERE u.id=$1`, buyer, key, owner, receipt.ID).Scan(&balance, &frozen, &pending, &quota, &c, &n, &f))
	require.Equal(t, "3.76543300", balance)
	require.Equal(t, "0.00000000", frozen)
	require.Equal(t, "0.98765360", pending)
	require.Equal(t, "1.23456700", quota)
	var accountQuota string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra->>'quota_used' FROM accounts WHERE id=$1`, account).Scan(&accountQuota))
	require.True(t, decimal.RequireFromString(accountQuota).Equal(decimal.RequireFromString("2.469134")))
	require.Equal(t, "1.23456700", c)
	require.Equal(t, "0.98765360", n)
	require.Equal(t, "0.24691340", f)
	var debit, credit string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COALESCE(sum(amount) FILTER (WHERE side='debit'),0)::text,COALESCE(sum(amount) FILTER (WHERE side='credit'),0)::text FROM ledger_entries WHERE transaction_id=$1`, result.LedgerTransactionID).Scan(&debit, &credit))
	require.Equal(t, "1.23456700", debit)
	require.Equal(t, debit, credit)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_outbox WHERE receipt_id=$1`, receipt.ID).Scan(&outbox))
	require.Equal(t, 1, outbox)

	// A failed projection rolls back its writes and remains retryable without recapturing.
	worker := &MarketplaceOutboxWorker{DB: integrationDB, Project: func(ctx context.Context, tx *sql.Tx, event MarketplaceOutboxEvent) error {
		require.Equal(t, buyer, event.BuyerUserID)
		require.Equal(t, key, event.APIKeyID)
		require.Equal(t, account, event.AccountID)
		require.True(t, event.APIKeyQuotaExhausted)
		require.True(t, event.APIKeyRateLimitChanged)
		require.Equal(t, "3.76543300", event.BuyerBalanceAfter.StringFixed(8))
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET quota_used=quota_used+100 WHERE id=$1`, key); err != nil {
			return err
		}
		return errors.New("projection failed")
	}}
	processed, err := worker.ProcessOne(ctx)
	require.True(t, processed)
	require.Error(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used::text FROM api_keys WHERE id=$1`, key).Scan(&quota))
	require.Equal(t, "1.23456700", quota)
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_outbox SET next_attempt_at=now() WHERE receipt_id=$1`, receipt.ID)
	require.NoError(t, err)
	worker.Project = func(context.Context, *sql.Tx, MarketplaceOutboxEvent) error { return nil }
	processed, err = worker.ProcessOne(ctx)
	require.True(t, processed)
	require.NoError(t, err)

	// Underfunding keeps trusted usage and the existing hold, without supplier earnings.
	next, _, err := requests.Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "next"})
	require.NoError(t, err)
	require.NoError(t, billing.ReserveBudget(ctx, next.ID, buyer, service.Money{Decimal: decimal.NewFromInt(1)}))
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	attempt, err = requests.CreateAttempt(ctx, tx, next.ID, q)
	require.NoError(t, err)
	require.NoError(t, requests.MarkDispatched(ctx, tx, next.ID, attempt.ID))
	require.NoError(t, tx.Commit())
	usage = service.UsageSnapshot{InputTokens: 10000000, Source: "upstream"}
	charge, err = service.NewMarketplaceQuoteService().CalculateActual(q, usage)
	require.NoError(t, err)
	net, fee, err = service.SplitMarketplaceCharge(charge, 2000)
	require.NoError(t, err)
	command = service.MarketplaceCaptureCommand{ReceiptID: next.ID, BillableAttemptID: attempt.ID, QuoteDigest: digest, Usage: usage, ConsumerCharge: charge, SupplierNet: net, PlatformFee: fee}
	review, err := billing.Capture(ctx, command, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.True(t, review.NeedsReview)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT u.frozen_balance::text,w.pending::text,r.status FROM users u JOIN supplier_wallets w ON w.user_id=$2 JOIN marketplace_requests r ON r.id=$3 WHERE u.id=$1`, buyer, owner, next.ID).Scan(&frozen, &pending, &status))
	require.Equal(t, "1.00000000", frozen)
	require.Equal(t, "0.98765360", pending)
	require.Equal(t, "needs_review", status)
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().Credit(ctx, tx, buyer, "credit:"+suffix, "test", decimal.NewFromInt(10), service.FundingCashPaid, "test-credit", suffix))
	require.NoError(t, tx.Commit())
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_wallets SET debt=1 WHERE user_id=$1`, owner)
	require.NoError(t, err)
	settled, err := billing.Capture(ctx, command, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.True(t, settled.FirstApplied)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, next.ID).Scan(&status))
	require.Equal(t, "settled", status)
	var debt string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pending::text,debt::text FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&pending, &debt))
	require.Equal(t, "7.98765360", pending)
	require.Equal(t, "0.00000000", debt)
	var offset string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT supplier_debt_offset::text FROM marketplace_captures WHERE receipt_id=$1`, next.ID).Scan(&offset))
	require.Equal(t, "1.00000000", offset)

	// A published zero price settles without a fake funding hold or supplier income.
	free, _, err := requests.Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "free"})
	require.NoError(t, err)
	zero := service.Money{Decimal: decimal.Zero}
	require.NoError(t, billing.ReserveBudget(ctx, free.ID, buyer, zero))
	freeQ := q
	freeQ.Tiers = []service.MarketplacePriceTier{{InputPerMillion: &zero, OutputPerMillion: &zero, CacheReadPerMillion: &zero, CacheWritePerMillion: &zero}}
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	attempt, err = requests.CreateAttempt(ctx, tx, free.ID, freeQ)
	require.NoError(t, err)
	require.NoError(t, requests.MarkDispatched(ctx, tx, free.ID, attempt.ID))
	require.NoError(t, tx.Commit())
	freeDigest, err := service.MarketplaceQuoteDigest(freeQ)
	require.NoError(t, err)
	freeCommand := service.MarketplaceCaptureCommand{ReceiptID: free.ID, BillableAttemptID: attempt.ID, QuoteDigest: freeDigest, Usage: service.UsageSnapshot{InputTokens: 10, Source: "upstream"}, ConsumerCharge: zero, SupplierNet: zero, PlatformFee: zero}
	freeResult, err := billing.Capture(ctx, freeCommand, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.True(t, freeResult.FirstApplied)
	require.Zero(t, freeResult.LedgerTransactionID)
	var holds int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM wallet_funding_operations WHERE operation_id=$1`, "marketplace:hold:"+free.ID).Scan(&holds))
	require.Zero(t, holds)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pending::text FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&pending))
	require.Equal(t, "7.98765360", pending)
	// A retry after later wallet activity returns the capture-time outcome.
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, NewWalletFundingRepository().Credit(ctx, tx, buyer, "later-credit:"+suffix, "test", decimal.NewFromInt(1), service.FundingCashPaid, "later-test-credit", suffix))
	require.NoError(t, tx.Commit())
	replay, err = billing.Capture(ctx, firstCommand, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.False(t, replay.FirstApplied)
	require.True(t, replay.APIKeyQuotaExhausted)
	require.True(t, replay.APIKeyRateLimitChanged)
	require.Equal(t, 3.765433, *replay.NewBalance)
}
