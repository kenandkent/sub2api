//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type staleRecoveryCache struct{}

func (staleRecoveryCache) InvalidateUserBalance(context.Context, int64) error     { return nil }
func (staleRecoveryCache) InvalidateAPIKeyRateLimit(context.Context, int64) error { return nil }

func TestMarketplaceBackgroundReviewsStaleReceiptsWithoutChangingFinancialEvidence(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var buyer, key, group, account, offer int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',10) RETURNING id`, suffix+"@example.test").Scan(&buyer))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,10,10)`, buyer, suffix)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, buyer, suffix).Scan(&key))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, buyer).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, account, buyer).Scan(&offer))
	requests := NewMarketplaceRequestRepository(integrationDB)
	makeReceipt := func(status string, stale bool, attemptStatus string) string {
		receipt, _, e := requests.Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: uuid.NewString()})
		require.NoError(t, e)
		if status != "prepared" {
			require.NoError(t, NewMarketplaceBillingRepository(integrationDB).ReserveBudget(ctx, receipt.ID, buyer, service.Money{Decimal: decimal.NewFromInt(1)}))
		}
		if attemptStatus != "" {
			var attempt int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status,usage) VALUES($1,1,$2,$3,$4,'{}','digest',$5,CASE WHEN $5='usage_recorded' THEN '{"input_tokens":42}'::jsonb ELSE NULL END) RETURNING id`, receipt.ID, account, offer, buyer, attemptStatus).Scan(&attempt))
			_, e = integrationDB.ExecContext(ctx, `UPDATE marketplace_requests SET billable_attempt_id=$2 WHERE id=$1`, receipt.ID, attempt)
			require.NoError(t, e)
		}
		age := "0 seconds"
		if stale {
			age = "3 hours"
		}
		_, e = integrationDB.ExecContext(ctx, `UPDATE marketplace_requests SET status=$2,updated_at=now()-$3::interval WHERE id=$1`, receipt.ID, status, age)
		require.NoError(t, e)
		return receipt.ID
	}
	stalePrepared := makeReceipt("prepared", true, "")
	staleReserved := makeReceipt("budget_reserved", true, "")
	staleDispatched := makeReceipt("dispatched", true, "usage_recorded")
	stalePending := makeReceipt("usage_pending", true, "possibly_executed")
	fresh := makeReceipt("dispatched", false, "possibly_executed")
	settled := makeReceipt("settled", true, "settled")
	reviewed := makeReceipt("needs_review", true, "possibly_executed")
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_requests SET review_reason='existing human review' WHERE id=$1`, reviewed)
	require.NoError(t, err)
	snapshot := func() (string, string, string, string, string) {
		var balance, frozen, lots, allocations, attempts string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance::text,frozen_balance::text FROM users WHERE id=$1`, buyer).Scan(&balance, &frozen))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY id),'[]')::text FROM wallet_funding_lots l WHERE user_id=$1`, buyer).Scan(&lots))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id),'[]')::text FROM wallet_funding_allocations a JOIN wallet_funding_lots l ON l.id=a.lot_id WHERE l.user_id=$1`, buyer).Scan(&allocations))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id)::text FROM marketplace_attempts a JOIN marketplace_requests r ON r.id=a.receipt_id WHERE r.buyer_user_id=$1`, buyer).Scan(&attempts))
		return balance, frozen, lots, allocations, attempts
	}
	b, f, l, a, at := snapshot()
	worker := NewMarketplaceBackgroundWorker(integrationDB, staleRecoveryCache{}, service.NewSupplierSettlementService(NewSupplierSettlementRepository(integrationDB)), service.NewSupplierFinanceService(NewSupplierFinanceRepository(integrationDB)))
	worker.Start()
	t.Cleanup(worker.Stop)
	require.Eventually(t, func() bool {
		var n int
		return integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_requests WHERE id IN ($1,$2,$3,$4) AND status='needs_review'`, stalePrepared, staleReserved, staleDispatched, stalePending).Scan(&n) == nil && n == 4
	}, 5*time.Second, 100*time.Millisecond)
	worker.Stop()
	for _, id := range []string{stalePrepared, staleReserved, staleDispatched, stalePending} {
		var reason string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT review_reason FROM marketplace_requests WHERE id=$1`, id).Scan(&reason))
		require.NotEmpty(t, reason)
		require.NotContains(t, reason, suffix)
	}
	for id, want := range map[string]string{fresh: "dispatched", settled: "settled", reviewed: "needs_review"} {
		var status string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, id).Scan(&status))
		require.Equal(t, want, status)
	}
	var reason string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT review_reason FROM marketplace_requests WHERE id=$1`, reviewed).Scan(&reason))
	require.Equal(t, "existing human review", reason)
	b2, f2, l2, a2, at2 := snapshot()
	require.Equal(t, b, b2)
	require.Equal(t, f, f2)
	require.Equal(t, l, l2)
	require.Equal(t, a, a2)
	require.Equal(t, at, at2)
}

func TestMarketplaceRecoveryReleasesReviewedUnattemptedHoldOnlyWithAuditedProof(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var buyer, admin, key, group, account, offer int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, suffix+"-buyer@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, suffix+"-admin@example.test").Scan(&admin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, buyer, suffix)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, buyer, suffix).Scan(&key))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, buyer).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, account, buyer).Scan(&offer))
	makeReview := func() string {
		r, _, e := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: uuid.NewString()})
		require.NoError(t, e)
		require.NoError(t, NewMarketplaceBillingRepository(integrationDB).ReserveBudget(ctx, r.ID, buyer, service.Money{Decimal: decimal.NewFromInt(1)}))
		_, e = integrationDB.ExecContext(ctx, `UPDATE marketplace_requests SET status='needs_review',review_reason='request inactive for over two hours; manual review required' WHERE id=$1`, r.ID)
		require.NoError(t, e)
		return r.ID
	}
	clean := makeReview()
	attempted := makeReview()
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status) VALUES($1,1,$2,$3,$4,'{}','digest','possibly_executed')`, attempted, account, offer, buyer)
	require.NoError(t, err)
	// An attempt inserted while the resolver is competing for the receipt lock
	// must be visible before any audited release is allowed.
	raced := makeReview()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var lockedID string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id::text FROM marketplace_requests WHERE id=$1 FOR UPDATE`, raced).Scan(&lockedID))
	_, err = tx.ExecContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status) VALUES($1,1,$2,$3,$4,'{}','digest','possibly_executed')`, raced, account, offer, buyer)
	require.NoError(t, err)
	repo := NewMarketplaceGatewayRepository(integrationDB, nil)
	started := make(chan struct{})
	resolved := make(chan error, 1)
	go func() {
		close(started)
		resolved <- repo.AdminRecoveryResolve(ctx, admin, raced, "release_confirmed_unexecuted", "proof", suffix+"-race")
	}()
	<-started
	require.NoError(t, tx.Commit())
	require.Error(t, <-resolved)
	require.Error(t, repo.AdminRecoveryResolve(ctx, buyer, clean, "release_confirmed_unexecuted", "proof", suffix+"-not-admin"))
	require.Error(t, repo.AdminRecoveryResolve(ctx, admin, clean, "release_confirmed_unexecuted", "", suffix+"-no-proof"))
	require.Error(t, repo.AdminRecoveryResolve(ctx, admin, attempted, "release_confirmed_unexecuted", "proof", suffix+"-attempted"))
	require.NoError(t, repo.AdminRecoveryResolve(ctx, admin, clean, "release_confirmed_unexecuted", "verified no dispatch", suffix+"-release"))
	require.NoError(t, repo.AdminRecoveryResolve(ctx, admin, clean, "release_confirmed_unexecuted", "verified no dispatch", suffix+"-release"))
	require.Error(t, repo.AdminRecoveryResolve(ctx, admin, clean, "release_confirmed_unexecuted", "different proof", suffix+"-release"))
	var status, frozen string
	var audits int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, clean).Scan(&status))
	require.Equal(t, "voided", status)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance::text FROM users WHERE id=$1`, buyer).Scan(&frozen))
	require.Equal(t, "2.00000000", frozen)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_recovery_audits WHERE receipt_id=$1`, clean).Scan(&audits))
	require.Equal(t, 1, audits)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, attempted).Scan(&status))
	require.Equal(t, "needs_review", status)
}

func TestMarketplaceStaleScanIsBoundedAndSkipsLockedReceipts(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var buyer, key, group int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, buyer, suffix).Scan(&key))
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = uuid.NewString()
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO marketplace_requests(id,buyer_user_id,api_key_id,group_id,request_digest,status,updated_at) VALUES($1,$2,$3,$4,'digest','prepared',now()-interval '3 hours')`, ids[i], buyer, key, group)
		require.NoError(t, err)
	}
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var locked string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT id::text FROM marketplace_requests WHERE id=$1 FOR UPDATE`, ids[0]).Scan(&locked))
	worker := &MarketplaceBackgroundWorker{db: integrationDB}
	n, err := worker.reviewStaleReceipts(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 100, n)
	var status string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, ids[0]).Scan(&status))
	require.Equal(t, "prepared", status)
	require.NoError(t, tx.Commit())
	n, err = worker.reviewStaleReceipts(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	n, err = worker.reviewStaleReceipts(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestMarketplaceStaleReviewAllowsKnownUsageCaptureAndReplay(t *testing.T) {
	isolateCommittedIntegrationTest(t)
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
	price := service.Money{Decimal: decimal.NewFromInt(2)}
	rate := decimal.RequireFromString("0.5")
	accountRate := decimal.NewFromInt(1)
	q := service.QuoteSnapshot{Currency: "USD", PublicModel: "m", ActualModel: "m", Protocol: "responses", PriceVersion: 1, PolicyVersion: 1, RateMultiplier: &rate, AccountRateMultiplier: &accountRate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}, FeeBPS: 2000, HoldHours: 24, AccountID: account, OfferID: offer, OwnerUserID: owner, SnapshotAt: time.Now().UTC()}
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	attempt, err := requests.CreateAttempt(ctx, tx, receipt.ID, q)
	require.NoError(t, err)
	require.NoError(t, requests.MarkDispatched(ctx, tx, receipt.ID, attempt.ID))
	require.NoError(t, tx.Commit())
	usage := service.UsageSnapshot{InputTokens: 1000000, Source: "upstream"}
	tx, err = integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, requests.RecordUsage(ctx, tx, receipt.ID, attempt.ID, usage))
	require.NoError(t, tx.Commit())
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_requests SET updated_at=now()-interval '3 hours' WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	n, err := (&MarketplaceBackgroundWorker{db: integrationDB}).reviewStaleReceipts(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	digest, err := service.MarketplaceQuoteDigest(q)
	require.NoError(t, err)
	charge, err := service.NewMarketplaceQuoteService().CalculateActual(q, usage)
	require.NoError(t, err)
	net, fee, err := service.SplitMarketplaceCharge(charge, q.FeeBPS)
	require.NoError(t, err)
	command := service.MarketplaceCaptureCommand{ReceiptID: receipt.ID, BillableAttemptID: attempt.ID, QuoteDigest: digest, Usage: usage, ConsumerCharge: charge, SupplierNet: net, PlatformFee: fee}
	result, err := billing.Capture(ctx, command, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.True(t, result.FirstApplied)
	replay, err := billing.Capture(ctx, command, service.UsageBillingCommand{})
	require.NoError(t, err)
	require.False(t, replay.FirstApplied)
	require.Equal(t, result.LedgerTransactionID, replay.LedgerTransactionID)
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1`, receipt.ID).Scan(&status))
	require.Equal(t, "settled", status)
	n, err = (&MarketplaceBackgroundWorker{db: integrationDB}).reviewStaleReceipts(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}
