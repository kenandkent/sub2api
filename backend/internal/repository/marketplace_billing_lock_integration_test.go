//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// These captures acquire their buyer locks concurrently before trying to use
// each other's user as the supplier FK. Both existing and new wallets matter.
func TestMarketplaceBillingReciprocalCapturesDoNotDeadlock(t *testing.T) {
	for _, walletsExist := range []bool{false, true} {
		t.Run(fmt.Sprintf("wallets_exist_%v", walletsExist), func(t *testing.T) {
			ctx := context.Background()
			suffix := uuid.NewString()
			var users, keys, accounts, offers [2]int64
			var group int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
			for i := range users {
				require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,balance) VALUES($1,'x',10) RETURNING id`, fmt.Sprintf("%s-%d@example.test", suffix, i)).Scan(&users[i]))
				_, err := integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_states(user_id) VALUES($1)`, users[i])
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(ctx, `INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','reciprocal-test',$2,10,10)`, users[i], fmt.Sprintf("%s-%d", suffix, i))
				require.NoError(t, err)
				require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, users[i], fmt.Sprintf("%s-%d", suffix, i)).Scan(&keys[i]))
				require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, fmt.Sprintf("%s-%d", suffix, i), users[i]).Scan(&accounts[i]))
				require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, accounts[i], users[i]).Scan(&offers[i]))
				if walletsExist {
					_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id) VALUES($1)`, users[i])
					require.NoError(t, err)
				}
			}
			requests := NewMarketplaceRequestRepository(integrationDB)
			billing := NewMarketplaceBillingRepository(integrationDB)
			var commands [2]service.MarketplaceCaptureCommand
			for i := range users {
				owner := 1 - i
				receipt, _, err := requests.Prepare(ctx, service.MarketplacePrepareCommand{BuyerUserID: users[i], APIKeyID: keys[i], GroupID: group, RequestDigest: fmt.Sprintf("digest-%d", i)})
				require.NoError(t, err)
				require.NoError(t, billing.ReserveBudget(ctx, receipt.ID, users[i], service.Money{Decimal: decimal.NewFromInt(1)}))
				price := service.Money{Decimal: decimal.NewFromInt(1)}
				rate := decimal.NewFromInt(1)
				q := service.QuoteSnapshot{Currency: "USD", PublicModel: "m", ActualModel: "m", Protocol: "responses", PriceVersion: 1, PolicyVersion: 1, RateMultiplier: &rate, AccountRateMultiplier: &rate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}, FeeBPS: 2000, AccountID: accounts[owner], OfferID: offers[owner], OwnerUserID: users[owner], SnapshotAt: time.Now().UTC()}
				tx, e := integrationDB.BeginTx(ctx, nil)
				require.NoError(t, e)
				a, e := requests.CreateAttempt(ctx, tx, receipt.ID, q)
				require.NoError(t, e)
				require.NoError(t, requests.MarkDispatched(ctx, tx, receipt.ID, a.ID))
				require.NoError(t, tx.Commit())
				digest, e := service.MarketplaceQuoteDigest(q)
				require.NoError(t, e)
				usage := service.UsageSnapshot{InputTokens: 1000000, Source: "upstream"}
				net, fee, e := service.SplitMarketplaceCharge(price, q.FeeBPS)
				require.NoError(t, e)
				commands[i] = service.MarketplaceCaptureCommand{ReceiptID: receipt.ID, BillableAttemptID: a.ID, QuoteDigest: digest, Usage: usage, ConsumerCharge: price, SupplierNet: net, PlatformFee: fee}
			}
			// Both transactions pause after locking their buyers and immediately before
			// inserting the supplier wallet. Releasing the advisory barrier makes the
			// reciprocal FK checks contend under the exact lock modes being tested.
			const barrier = 1747382039
			_, err := integrationDB.ExecContext(ctx, `CREATE OR REPLACE FUNCTION marketplace_test_capture_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(1747382039); RETURN NEW; END $$`)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `CREATE TRIGGER marketplace_test_capture_barrier BEFORE INSERT ON supplier_wallets FOR EACH ROW EXECUTE FUNCTION marketplace_test_capture_barrier()`)
			require.NoError(t, err)
			defer func() {
				_, _ = integrationDB.ExecContext(ctx, `DROP TRIGGER IF EXISTS marketplace_test_capture_barrier ON supplier_wallets`)
				_, _ = integrationDB.ExecContext(ctx, `DROP FUNCTION IF EXISTS marketplace_test_capture_barrier()`)
			}()
			conn, err := integrationDB.Conn(ctx)
			require.NoError(t, err)
			defer conn.Close()
			_, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, barrier)
			require.NoError(t, err)
			defer conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, barrier)
			errs := make(chan error, 2)
			for i := range commands {
				go func(i int) { _, e := billing.Capture(ctx, commands[i], service.UsageBillingCommand{}); errs <- e }(i)
			}
			require.Eventually(t, func() bool {
				var count int
				e := integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND NOT granted`, barrier).Scan(&count)
				return e == nil && count == 2
			}, 10*time.Second, 20*time.Millisecond)
			_, err = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, barrier)
			require.NoError(t, err)
			for range commands {
				select {
				case e := <-errs:
					require.NoError(t, e)
				case <-time.After(10 * time.Second):
					t.Fatal("reciprocal capture timed out")
				}
			}
			for _, c := range commands {
				r, e := billing.Capture(ctx, c, service.UsageBillingCommand{})
				require.NoError(t, e)
				require.False(t, r.FirstApplied)
				_, e = integrationDB.ExecContext(ctx, `UPDATE marketplace_outbox SET processed_at=now() WHERE receipt_id=$1`, c.ReceiptID)
				require.NoError(t, e)
			}
		})
	}
}
