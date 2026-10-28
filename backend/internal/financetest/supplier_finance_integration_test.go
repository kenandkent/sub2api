//go:build integration

package financetest

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFinanceStatementBoundaryOwnershipAndProjection(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	receipt := uuid.NewString()
	var owner, other, buyer, key, group, account, offer, attempt int64
	for _, v := range []struct {
		dst   *int64
		label string
	}{{&owner, "owner"}, {&other, "other"}, {&buyer, "buyer"}} {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+v.label+"@example.test").Scan(v.dst))
	}
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'suspended'),($2,'active')`, owner, other)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id,pending) VALUES($1,7)`, owner)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'key') RETURNING id`, buyer, suffix).Scan(&key))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES('=unsafe','openai','apikey',$1) RETURNING id`, owner).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, account, owner).Scan(&offer))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_requests(id,buyer_user_id,api_key_id,group_id,request_digest,status) VALUES($1,$2,$3,$4,'digest','settled')`, receipt, buyer, key, group)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status) VALUES($1,1,$2,$3,$4,'{"public_model":"model"}','digest','settled') RETURNING id`, receipt, account, offer, owner).Scan(&attempt))
	post := func(op string, at time.Time, net, fee string, refund bool) int64 {
		_, e := integrationDB.ExecContext(ctx, `ALTER TABLE ledger_transactions DISABLE TRIGGER marketplace_ledger_booking_period`)
		require.NoError(t, e)
		defer func() {
			_, _ = integrationDB.ExecContext(ctx, `ALTER TABLE ledger_transactions ENABLE TRIGGER marketplace_ledger_booking_period`)
		}()
		tx, e := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, e)
		defer tx.Rollback()
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint,created_at,booked_at) VALUES($1,'test','USD','test',$2,$2) RETURNING id`, op, at).Scan(&id))
		side1, side2 := "credit", "debit"
		if refund {
			side1, side2 = side2, side1
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO ledger_entries(transaction_id,account,subject_id,side,amount) VALUES($1,'supplier_pending',$2,$3,$4),($1,'platform_fee',NULL,$3,$5),($1,'marketplace_clearing',NULL,$6,$4::numeric+$5::numeric)`, id, owner, side1, net, fee, side2)
		require.NoError(t, e)
		_, e = tx.ExecContext(ctx, `UPDATE ledger_transactions SET sealed=true WHERE id=$1`, id)
		require.NoError(t, e)
		require.NoError(t, tx.Commit())
		return id
	}
	feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	march := feb.AddDate(0, 1, 0)
	captureTx := post("test:finance:capture:"+suffix, feb, "8", "2", false)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_captures(receipt_id,attempt_id,owner_user_id,consumer_charge,supplier_net,platform_fee,matures_at,ledger_transaction_id,fingerprint,buyer_balance_after) VALUES($1,$2,$3,10,8,2,now(),$4,'test',0)`, receipt, attempt, owner, captureTx)
	require.NoError(t, err)
	repo := repository.NewSupplierFinanceRepository(integrationDB)
	first, err := repo.GenerateClosedMonth(ctx, owner, feb)
	require.NoError(t, err)
	require.Equal(t, "8.00000000", first.ClosingPending)
	require.Equal(t, "8.00000000", first.EarnedNet)
	require.Equal(t, "2.00000000", first.PlatformFee)
	entries, err := repo.StatementEntries(ctx, owner, first.ID, 1, 20)
	require.NoError(t, err)
	require.Len(t, entries.Items, 1)
	require.Equal(t, "=unsafe", entries.Items[0].AccountName)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := handler.NewSupplierFinanceHandler(service.NewSupplierFinanceService(repo), nil, nil)
	router.GET("/:id/export.csv", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: owner})
		h.ExportStatement(c)
	})
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/%d/export.csv", first.ID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "'=unsafe")
	require.False(t, strings.Contains(response.Body.String(), "buyer"))
	refundTx := post("test:finance:refund:"+suffix, march, "1", "0.25", true)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_refunds(operation_id,receipt_id,actor_user_id,amount,supplier_amount,platform_amount,ledger_transaction_id) VALUES($1,$2,$3,1.25,1,.25,$4)`, suffix, receipt, owner, refundTx)
	require.NoError(t, err)
	again, err := repo.GenerateClosedMonth(ctx, owner, feb)
	require.NoError(t, err)
	require.Equal(t, first, again)
	next, err := repo.GenerateClosedMonth(ctx, owner, march)
	require.NoError(t, err)
	require.Equal(t, "8.00000000", next.OpeningPending)
	require.Equal(t, "7.00000000", next.ClosingPending)
	require.Equal(t, "1.00000000", next.RefundNet)
	originalEntries, err := repo.StatementEntries(ctx, owner, first.ID, 1, 20)
	require.NoError(t, err)
	require.Len(t, originalEntries.Items, 1)
	refundEntries, err := repo.StatementEntries(ctx, owner, next.ID, 1, 20)
	require.NoError(t, err)
	require.Len(t, refundEntries.Items, 1)
	require.Equal(t, "refund", refundEntries.Items[0].Kind)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_statements SET earned_net=99 WHERE id=$1`, first.ID)
	require.Error(t, err)
	_, err = repo.Statement(ctx, other, first.ID)
	require.ErrorIs(t, err, service.ErrSupplierFinanceNotFound)
	list, err := repo.Earnings(ctx, owner, service.FinanceFilter{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, "=unsafe", list.Items[0].AccountName)
	empty, err := repo.Earnings(ctx, other, service.FinanceFilter{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Empty(t, empty.Items)
	require.Equal(t, "0", empty.Wallet.Pending)
	_, err = repo.Earnings(ctx, buyer, service.FinanceFilter{Page: 1, PageSize: 20})
	require.ErrorIs(t, err, service.ErrSupplierFinanceNotFound)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_wallets SET pending=6 WHERE user_id=$1`, owner)
	require.NoError(t, err)
	rec, err := repo.Reconciliation(ctx, "projection_difference", 1, 100)
	require.NoError(t, err)
	found := false
	for _, i := range rec.Items {
		if i.Kind == "wallet" && i.ID == fmt.Sprint(owner) {
			found = true
		}
	}
	require.True(t, found)
}

func TestConcurrentStatementGeneration(t *testing.T) {
	ctx := context.Background()
	var owner int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, uuid.NewString()+"@example.test").Scan(&owner))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, owner)
	require.NoError(t, err)
	repo := repository.NewSupplierFinanceRepository(integrationDB)
	month := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	ids := make(chan int64, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := repo.GenerateClosedMonth(ctx, owner, month)
			errs <- e
			if e == nil {
				ids <- s.ID
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for e := range errs {
		require.NoError(t, e)
	}
	var previous int64
	for id := range ids {
		if previous != 0 {
			require.Equal(t, previous, id)
		}
		previous = id
	}
	require.NotZero(t, previous)
}

func TestAdminStatementGenerationIdempotency(t *testing.T) {
	ctx := context.Background()
	var owner, admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, uuid.NewString()+"@example.test").Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, uuid.NewString()+"@example.test").Scan(&admin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id) VALUES($1)`, owner)
	require.NoError(t, err)
	repo := repository.NewSupplierFinanceRepository(integrationDB)
	month := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	operation := uuid.NewString()
	first, err := repo.AdminGenerateClosedMonth(ctx, admin, owner, month, operation, "month close")
	require.NoError(t, err)
	again, err := repo.AdminGenerateClosedMonth(ctx, admin, owner, month, operation, "month close")
	require.NoError(t, err)
	require.Equal(t, first, again)
	_, err = repo.AdminGenerateClosedMonth(ctx, admin, owner, month, operation, "changed")
	require.ErrorIs(t, err, service.ErrSupplierFinanceConflict)
}

func TestStatementCloseWaitsForBookedWriter(t *testing.T) {
	ctx := context.Background()
	var owner int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, uuid.NewString()+"@example.test").Scan(&owner))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id) VALUES($1)`, owner)
	require.NoError(t, err)
	// Only the isolated fixture bypasses the clock trigger to represent an open
	// writer in an already elapsed month; it still holds the real period lock.
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE ledger_transactions DISABLE TRIGGER marketplace_ledger_booking_period`)
	require.NoError(t, err)
	defer func() {
		_, _ = integrationDB.ExecContext(ctx, `ALTER TABLE ledger_transactions ENABLE TRIGGER marketplace_ledger_booking_period`)
	}()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	month := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('marketplace-period:2024-04',0))`)
	require.NoError(t, err)
	var ledger int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint,created_at,booked_at) VALUES($1,'test','USD','test',$2,$2) RETURNING id`, uuid.NewString(), month).Scan(&ledger))
	_, err = tx.ExecContext(ctx, `INSERT INTO ledger_entries(transaction_id,account,subject_id,side,amount) VALUES($1,'supplier_pending',$2,'credit',5),($1,'marketplace_clearing',NULL,'debit',5)`, ledger, owner)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE ledger_transactions SET sealed=true WHERE id=$1`, ledger)
	require.NoError(t, err)
	type result struct {
		s   *service.SupplierStatement
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, e := repository.NewSupplierFinanceRepository(integrationDB).GenerateClosedMonth(ctx, owner, month)
		done <- result{s, e}
	}()
	select {
	case <-done:
		t.Fatal("close completed before writer committed")
	case <-time.After(200 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, "5.00000000", got.s.ClosingPending)
	case <-time.After(10 * time.Second):
		t.Fatal("close did not resume")
	}
}

func TestLedgerBookingUsesDatabaseClockAndCannotChange(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	var id int64
	var booked time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint,booked_at) VALUES($1,'test','USD','test','2000-01-01') RETURNING id,booked_at`, uuid.NewString()).Scan(&id, &booked))
	require.WithinDuration(t, time.Now(), booked, 5*time.Second)
	_, err = tx.ExecContext(ctx, `INSERT INTO ledger_entries(transaction_id,account,side,amount) VALUES($1,'test_a','debit',1),($1,'test_b','credit',1)`, id)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE ledger_transactions SET sealed=true WHERE id=$1`, id)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	_, err = integrationDB.ExecContext(ctx, `UPDATE ledger_transactions SET booked_at=booked_at-interval '1 day' WHERE id=$1`, id)
	require.Error(t, err)
}
