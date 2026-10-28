//go:build integration

package repository

import (
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestMaturityConcurrentRepeatAndFreeze(t *testing.T) {
	ctx := context.Background()
	suffix := uuid.NewString()
	receipt := uuid.NewString()
	var buyer, owner, key, group, account, offer, attempt, capture int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"b@example.test").Scan(&buyer))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"s@example.test").Scan(&owner))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,payout_frozen) VALUES($1,true)`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id,pending) VALUES($1,5)`, owner)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, buyer, suffix).Scan(&key))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix).Scan(&group))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, owner).Scan(&account))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id) VALUES($1,$2) RETURNING id`, account, owner).Scan(&offer))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO marketplace_requests(id,buyer_user_id,api_key_id,group_id,request_digest,status) VALUES($1,$2,$3,$4,'test','settled')`, receipt, buyer, key, group)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO marketplace_attempts(receipt_id,ordinal,account_id,offer_id,owner_user_id,quote,quote_digest,status) VALUES($1,1,$2,$3,$4,'{}','test','settled') RETURNING id`, receipt, account, offer, owner).Scan(&attempt))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO marketplace_captures(receipt_id,attempt_id,owner_user_id,consumer_charge,supplier_net,platform_fee,supplier_debt_offset,refunded_charge,refunded_net,refunded_fee,matures_at,fingerprint) VALUES($1,$2,$3,12,10,2,3,2.4,2,.4,now()-interval '1 hour','test') RETURNING id`, receipt, attempt, owner).Scan(&capture))
	repo := NewSupplierSettlementRepository(integrationDB)
	applied, err := repo.MatureCapture(ctx, capture, time.Now())
	require.NoError(t, err)
	require.False(t, applied)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_profiles SET payout_frozen=false WHERE user_id=$1`, owner)
	require.NoError(t, err)
	applied, err = repo.MatureCapture(ctx, capture, time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	require.False(t, applied)
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := repo.MatureCapture(ctx, capture, time.Now())
			results <- ok
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	require.Equal(t, 1, count)
	var pending, available string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT pending::text,available::text FROM supplier_wallets WHERE user_id=$1`, owner).Scan(&pending, &available))
	require.Equal(t, "0.00000000", pending)
	require.Equal(t, "5.00000000", available)
	var transactions int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM ledger_transactions WHERE operation_id=$1`, "marketplace:maturity:"+receipt).Scan(&transactions))
	require.Equal(t, 1, transactions)
}
