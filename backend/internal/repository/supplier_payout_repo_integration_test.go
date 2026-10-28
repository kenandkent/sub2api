//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestPayoutConcurrentRequestsCannotExceedAvailable(t *testing.T) {
	ctx := context.Background()
	var owner, admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES ($1,'x') RETURNING id`, fmt.Sprintf("payout-%d@example.com", time.Now().UnixNano())).Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES ($1,'x','admin') RETURNING id`, fmt.Sprintf("payout-admin-%d@example.com", time.Now().UnixNano())).Scan(&admin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES ($1,'active')`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id,available) VALUES ($1,10.12345678)`, owner)
	require.NoError(t, err)
	repo := NewSupplierPayoutRepository(integrationDB)
	envelope, err := service.NewCredentialEnvelope("k", map[string][]byte{"k": make([]byte, 32)})
	require.NoError(t, err)
	d, err := repo.CreateDestination(ctx, owner, "bank ending 1234", []byte("secret"), "destination-concurrency", envelope)
	require.NoError(t, err)
	_, err = repo.VerifyDestination(ctx, d.ID, admin, "verify-concurrency")
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10.00"), fmt.Sprintf("request-concurrency-%d", i))
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
	var available, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT available,payout_reserved FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&available, &reserved))
	require.True(t, available.Equal(decimal.RequireFromString("0.12345678")))
	require.True(t, reserved.Equal(decimal.RequireFromString("10")))
}

func payoutFixture(t *testing.T) (*SupplierPayoutRepository, int64, int64, *service.PayoutDestination) {
	t.Helper()
	ctx := context.Background()
	var owner, admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES ($1,'x','admin') RETURNING id`, fmt.Sprintf("owner-%d@example.com", time.Now().UnixNano())).Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES ($1,'x','admin') RETURNING id`, fmt.Sprintf("admin-%d@example.com", time.Now().UnixNano())).Scan(&admin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES ($1,'active')`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id,available) VALUES ($1,50.12345678)`, owner)
	require.NoError(t, err)
	repo := NewSupplierPayoutRepository(integrationDB)
	env, err := service.NewCredentialEnvelope("k", map[string][]byte{"k": make([]byte, 32)})
	require.NoError(t, err)
	d, err := repo.CreateDestination(ctx, owner, "ending 1234", []byte("secret"), fmt.Sprintf("dest-%d", owner), env)
	require.NoError(t, err)
	_, err = repo.VerifyDestination(ctx, d.ID, owner, fmt.Sprintf("self-verify-%d", owner))
	require.Error(t, err)
	d, err = repo.VerifyDestination(ctx, d.ID, admin, fmt.Sprintf("verify-%d", owner))
	require.NoError(t, err)
	return repo, owner, admin, d
}
func TestPayoutStateMachineAndRefundRelease(t *testing.T) {
	ctx := context.Background()
	repo, owner, admin, d := payoutFixture(t)
	op := func(n string) string { return fmt.Sprintf("%s-%d", n, owner) }
	p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op("request"))
	require.NoError(t, err)
	again, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op("request"))
	require.NoError(t, err)
	require.Equal(t, p.ID, again.ID)
	_, err = repo.Request(ctx, owner, d.ID, decimal.RequireFromString("9"), op("request"))
	require.Error(t, err)
	action := func(name string, actor int64, evidence string) (*service.SupplierPayout, error) {
		return repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: actor, OperationID: op(name), Action: name, Evidence: evidence})
	}
	_, err = action("approve", owner, "")
	require.Error(t, err)
	p, err = action("approve", admin, "")
	require.NoError(t, err)
	require.Equal(t, "approved", p.Status)
	p, err = action("start", admin, "")
	require.NoError(t, err)
	require.Equal(t, "processing", p.Status)
	_, err = action("cancel", admin, "uncertain")
	require.Error(t, err)
	_, err = action("fail", admin, "")
	require.Error(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_wallets SET debt=3 WHERE user_id=$1`, owner)
	require.NoError(t, err)
	p, err = action("fail", admin, "bank confirms no transfer")
	require.NoError(t, err)
	require.Equal(t, "failed_confirmed", p.Status)
	var debt, available, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT debt,available,payout_reserved FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&debt, &available, &reserved))
	require.True(t, debt.IsZero())
	require.True(t, available.Equal(decimal.RequireFromString("47.12345678")))
	require.True(t, reserved.IsZero())
}
func TestPayoutRiskCancellationAndStartRechecks(t *testing.T) {
	ctx := context.Background()
	repo, owner, admin, d := payoutFixture(t)
	op := func(n string) string { return fmt.Sprintf("%s-%d", n, owner) }
	p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op("request"))
	require.NoError(t, err)
	_, err = repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op("approve"), Action: "approve"})
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT id FROM supplier_wallets WHERE user_id=$1 FOR UPDATE`, owner)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE supplier_wallets SET debt=12 WHERE user_id=$1`, owner)
	require.NoError(t, err)
	require.NoError(t, repo.CancelPreProcessingTx(ctx, tx, owner, op("refund"), "admin:refund"))
	require.NoError(t, tx.Commit())
	_, err = repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op("start"), Action: "start"})
	require.Error(t, err)
	got, err := repo.Get(ctx, p.ID, owner, false)
	require.NoError(t, err)
	require.Equal(t, "risk_cancelled", got.Status)
	var debt, reserved decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT debt,payout_reserved FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&debt, &reserved))
	require.True(t, debt.Equal(decimal.RequireFromString("2")))
	require.True(t, reserved.IsZero())
}
func TestPayoutPaidRequiresMatchingEvidenceAndUniqueReference(t *testing.T) {
	ctx := context.Background()
	repo, owner, admin, d := payoutFixture(t)
	op := func(n string) string { return fmt.Sprintf("%s-%d", n, owner) }
	create := func(n string) *service.SupplierPayout {
		p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op("request"+n))
		require.NoError(t, err)
		for _, act := range []string{"approve", "start"} {
			_, err = repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op(act + n), Action: act})
			require.NoError(t, err)
		}
		return p
	}
	p := create("a")
	now := time.Now().UTC()
	a := service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op("confirm-a"), Action: "confirm", Evidence: "verified transfer", ExternalReference: op("bank-ref"), Currency: "USD", Amount: decimal.RequireFromString("9"), PaidAt: &now}
	_, err := repo.Action(ctx, a)
	require.Error(t, err)
	a.Amount = decimal.RequireFromString("10")
	a.ActorID = owner
	_, err = repo.Action(ctx, a)
	require.Error(t, err)
	a.ActorID = admin
	paid, err := repo.Action(ctx, a)
	require.NoError(t, err)
	require.Equal(t, "paid", paid.Status)
	retry, err := repo.Action(ctx, a)
	require.NoError(t, err)
	require.Equal(t, paid.ID, retry.ID)
	require.Equal(t, "paid", retry.Status)
	a.PayoutID = create("b").ID
	a.OperationID = op("confirm-b")
	_, err = repo.Action(ctx, a)
	require.Error(t, err)
}

func TestPayoutRetryReturnsOriginalWhenNewPayoutsDisabled(t *testing.T) {
	ctx := context.Background()
	repo, owner, _, d := payoutFixture(t)
	op := fmt.Sprintf("disabled-retry-%d", owner)
	p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op)
	require.NoError(t, err)
	svc := service.NewSupplierPayoutService(repo, service.NewMarketplacePolicyService(nil), nil)
	retry, err := svc.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op)
	require.NoError(t, err)
	require.Equal(t, p.ID, retry.ID)
	_, err = svc.Request(ctx, owner, d.ID, decimal.RequireFromString("11"), op)
	require.Error(t, err)
}

func TestPayoutDestinationEncryptedVersionAndImmutableSnapshot(t *testing.T) {
	ctx := context.Background()
	repo, owner, _, d := payoutFixture(t)
	env, err := service.NewCredentialEnvelope("k", map[string][]byte{"k": make([]byte, 32)})
	require.NoError(t, err)
	var raw string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credential::text FROM supplier_payout_destinations WHERE id=$1`, d.ID).Scan(&raw))
	require.NotContains(t, raw, "secret")
	retry, err := repo.CreateDestination(ctx, owner, "ending 1234", []byte("secret"), fmt.Sprintf("dest-%d", owner), env)
	require.NoError(t, err)
	require.Equal(t, d.ID, retry.ID)
	_, err = repo.CreateDestination(ctx, owner, "ending 1234", []byte("different"), fmt.Sprintf("dest-%d", owner), env)
	require.Error(t, err)
	d2, err := repo.CreateDestination(ctx, owner, "ending 5678", []byte("new secret"), fmt.Sprintf("dest2-%d", owner), env)
	require.NoError(t, err)
	require.Equal(t, d.Version+1, d2.Version)
	require.False(t, d2.Verified)
	p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), fmt.Sprintf("snapshot-%d", owner))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_payouts SET amount=11 WHERE id=$1`, p.ID)
	require.Error(t, err)
}

func TestPayoutRefundAndStartSerializeOnWallet(t *testing.T) {
	ctx := context.Background()
	repo, owner, admin, d := payoutFixture(t)
	op := func(n string) string { return fmt.Sprintf("%s-%d", n, owner) }
	p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op("request"))
	require.NoError(t, err)
	_, err = repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op("approve"), Action: "approve"})
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT id FROM supplier_wallets WHERE user_id=$1 FOR UPDATE`, owner)
	require.NoError(t, err)
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, e := repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op("start"), Action: "start"})
		result <- e
	}()
	<-started
	_, err = tx.ExecContext(ctx, `UPDATE supplier_wallets SET debt=3 WHERE user_id=$1`, owner)
	require.NoError(t, err)
	require.NoError(t, repo.CancelPreProcessingTx(ctx, tx, owner, op("refund"), "admin:refund"))
	require.NoError(t, tx.Commit())
	require.Error(t, <-result)
	got, err := repo.Get(ctx, p.ID, owner, false)
	require.NoError(t, err)
	require.Equal(t, "risk_cancelled", got.Status)
}
func TestPayoutStartRechecksFreeze(t *testing.T) {
	ctx := context.Background()
	repo, owner, admin, d := payoutFixture(t)
	op := func(n string) string { return fmt.Sprintf("%s-%d", n, owner) }
	p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), op("request"))
	require.NoError(t, err)
	_, err = repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op("approve"), Action: "approve"})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_profiles SET payout_frozen=true WHERE user_id=$1`, owner)
	require.NoError(t, err)
	_, err = repo.Action(ctx, service.PayoutAction{PayoutID: p.ID, ActorID: admin, OperationID: op("start"), Action: "start"})
	require.Error(t, err)
	got, err := repo.Get(ctx, p.ID, owner, false)
	require.NoError(t, err)
	require.Equal(t, "approved", got.Status)
}

func TestPayoutRequestKeyCannotCollideWithRefundLedgerOperation(t *testing.T) {
	ctx := context.Background()
	repo, owner, _, d := payoutFixture(t)
	p, err := repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), fmt.Sprintf("request-%d", owner))
	require.NoError(t, err)
	_, err = repo.Request(ctx, owner, d.ID, decimal.RequireFromString("10"), fmt.Sprintf("risk:refund-%d:%d", owner, p.ID))
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT id FROM supplier_wallets WHERE user_id=$1 FOR UPDATE`, owner)
	require.NoError(t, err)
	require.NoError(t, repo.CancelPreProcessingTx(ctx, tx, owner, fmt.Sprintf("refund-%d", owner), "admin:refund"))
	require.NoError(t, tx.Commit())
}
