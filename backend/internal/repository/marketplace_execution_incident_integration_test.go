//go:build integration

package repository

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestExecutionIncidentRecordsEvidenceWithoutFinancialEffects(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var buyer, admin, otherAdmin, key, group, account, offer int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, suffix+"-buyer@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, suffix+"-admin@example.test").Scan(&admin))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, suffix+"-admin2@example.test").Scan(&otherAdmin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, buyer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, buyer, suffix)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, buyer, suffix).Scan(&key))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, buyer).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, account, buyer).Scan(&offer))
	makeReleased := func(tag string) (string, int64, string) {
		req, _, e := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: tag})
		require.NoError(t, e)
		require.NoError(t, NewMarketplaceBillingRepository(integrationDB).ReserveBudget(ctx, req.ID, buyer, service.Money{Decimal: decimal.NewFromInt(1)}))
		var attempt int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status) VALUES($1,1,$2,$3,$4,'{}','snapshot-digest','possibly_executed') RETURNING id`, req.ID, account, offer, buyer).Scan(&attempt))
		_, e = integrationDB.ExecContext(ctx, `UPDATE marketplace_requests SET status='dispatched',billable_attempt_id=$2 WHERE id=$1`, req.ID, attempt)
		require.NoError(t, e)
		op := suffix + "-release-" + tag
		require.NoError(t, NewMarketplaceGatewayRepository(integrationDB, nil).AdminRecoveryResolve(ctx, admin, req.ID, "release_confirmed_unexecuted", "release proof", op))
		return req.ID, attempt, op
	}
	receipt, attempt, release := makeReleased("first")
	other, otherAttempt, otherRelease := makeReleased("second")
	fresh, _, err := NewMarketplaceRequestRepository(integrationDB).Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: buyer, APIKeyID: key, GroupID: group, RequestDigest: "fresh"})
	require.NoError(t, err)
	snapshot := func() string {
		var result string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT jsonb_build_object('user',(SELECT to_jsonb(u) FROM users u WHERE id=$1),'lots',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM wallet_funding_lots l WHERE user_id=$1),'allocations',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM wallet_funding_allocations a JOIN wallet_funding_lots l ON l.id=a.lot_id WHERE l.user_id=$1),'captures',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM marketplace_captures c),'wallets',(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM supplier_wallets w),'ledger',(SELECT count(*) FROM ledger_transactions),'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM marketplace_requests r WHERE r.buyer_user_id=$1),'audits',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.operation_id) FROM marketplace_recovery_audits x JOIN marketplace_requests r ON r.id=x.receipt_id WHERE r.buyer_user_id=$1),'attempts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM marketplace_attempts a JOIN marketplace_requests r ON r.id=a.receipt_id WHERE r.buyer_user_id=$1))::text`, buyer).Scan(&result))
		return result
	}
	// Another audit action on the same receipt is not a release audit.
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_recovery_audits(operation_id,actor_user_id,receipt_id,action,evidence) VALUES($1,$2,$3,'retry_known_usage','prior evidence')`, suffix+"-other-action", admin, receipt)
	require.NoError(t, err)
	before := snapshot()
	repo := NewMarketplaceGatewayRepository(integrationDB, nil)
	op := suffix + "-incident"
	require.ErrorIs(t, func() error {
		_, e := repo.RegisterExecutionIncident(ctx, buyer, receipt, attempt, release, "evidence", op)
		return e
	}(), service.ErrSupplierFinanceInvalid)
	require.ErrorIs(t, func() error {
		_, e := repo.RegisterExecutionIncident(ctx, admin, receipt, otherAttempt, release, "evidence", op)
		return e
	}(), service.ErrSupplierFinanceInvalid)
	require.ErrorIs(t, func() error {
		_, e := repo.RegisterExecutionIncident(ctx, admin, receipt, attempt, otherRelease, "evidence", op)
		return e
	}(), service.ErrSupplierFinanceInvalid)
	require.ErrorIs(t, func() error {
		_, e := repo.RegisterExecutionIncident(ctx, admin, fresh.ID, attempt, release, "evidence", op)
		return e
	}(), service.ErrSupplierFinanceInvalid)
	require.ErrorIs(t, func() error {
		_, e := repo.RegisterExecutionIncident(ctx, admin, receipt, attempt, release, strings.Repeat("x", 4001), op)
		return e
	}(), service.ErrSupplierFinanceInvalid)
	require.ErrorIs(t, func() error { _, e := repo.ListExecutionIncidents(ctx, buyer, receipt); return e }(), service.ErrSupplierFinanceInvalid)
	_, err = repo.RegisterExecutionIncident(ctx, admin, receipt, attempt, suffix+"-other-action", "evidence", op)
	require.ErrorIs(t, err, service.ErrSupplierFinanceInvalid)
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, otherAdmin)
	require.NoError(t, err)
	_, err = repo.RegisterExecutionIncident(ctx, otherAdmin, receipt, attempt, release, "evidence", op)
	require.ErrorIs(t, err, service.ErrSupplierFinanceInvalid)
	_, err = repo.ListExecutionIncidents(ctx, otherAdmin, receipt)
	require.ErrorIs(t, err, service.ErrSupplierFinanceInvalid)
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET status='active' WHERE id=$1`, otherAdmin)
	require.NoError(t, err)
	v, err := repo.RegisterExecutionIncident(ctx, admin, receipt, attempt, release, " provider evidence ", op)
	require.NoError(t, err)
	require.Equal(t, receipt, v.ReceiptID)
	require.Equal(t, attempt, v.AttemptID)
	require.Equal(t, "snapshot-digest", v.QuoteDigest)
	require.Equal(t, "provider evidence", v.Evidence)
	require.Equal(t, "recorded_manual_review", v.Status)
	replay, err := repo.RegisterExecutionIncident(ctx, admin, receipt, attempt, release, "provider evidence", op)
	require.NoError(t, err)
	require.Equal(t, v, replay)
	_, err = repo.RegisterExecutionIncident(ctx, admin, receipt, attempt, release, "changed", op)
	require.ErrorIs(t, err, service.ErrSupplierFinanceConflict)
	_, err = repo.RegisterExecutionIncident(ctx, otherAdmin, receipt, attempt, release, "provider evidence", op)
	require.ErrorIs(t, err, service.ErrSupplierFinanceConflict)
	items, err := repo.ListExecutionIncidents(ctx, admin, receipt)
	require.NoError(t, err)
	require.Equal(t, []service.MarketplaceExecutionIncident{*v}, items)
	// Concurrent retries using the same operation commit a single incident.
	var wg sync.WaitGroup
	results := make([]*service.MarketplaceExecutionIncident, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.RegisterExecutionIncident(ctx, admin, receipt, attempt, release, "concurrent evidence", suffix+"-concurrent")
		}(i)
	}
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i])
	}
	items, err = repo.ListExecutionIncidents(ctx, admin, receipt)
	require.NoError(t, err)
	require.Len(t, items, 2)
	items, err = repo.ListExecutionIncidents(ctx, admin, other)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Equal(t, before, snapshot())
	_, err = integrationDB.ExecContext(ctx, `UPDATE marketplace_execution_incidents SET evidence='tampered' WHERE operation_id=$1`, op)
	require.Error(t, err)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM marketplace_execution_incidents WHERE operation_id=$1`, op)
	require.Error(t, err)
}
