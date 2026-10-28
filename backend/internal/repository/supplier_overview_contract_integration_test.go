//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSupplierOverviewIncludesDashboardValues(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	var owner, account int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"@overview.test").Scan(&owner))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, owner)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id,pending,available,payout_reserved) VALUES($1,0.12345678,4.25,1)`, owner)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, owner).Scan(&account))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id,review_status,enabled) VALUES($1,$2,'approved',true)`, account, owner)
	require.NoError(t, err)
	v, err := NewSupplierAccountRepository(integrationDB, nil).Overview(ctx, owner)
	require.NoError(t, err)
	require.EqualValues(t, 1, v.OnlineAccounts)
	require.Equal(t, "0.12345678", v.Pending)
	require.Equal(t, "4.25000000", v.Available)
	require.Equal(t, "1.00000000", v.PayoutReserved)
	require.Equal(t, []int64{account}, v.AccountIDs)
	items, _, err := NewSupplierAccountRepository(integrationDB, nil).List(ctx, owner, service.SupplierAccountFilter{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "0", items[0].Usage.ConsumerCharge)
	require.Equal(t, "0", items[0].Usage.SupplierNet)
}

func TestSupplierAccountListUsageMatchesCapturedDetail(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	_,_,_,owner,_,_,_:=paymentRefundCaptureFixture(t)
	repo:=NewSupplierAccountRepository(integrationDB,nil)
	items,_,err:=repo.List(context.Background(),owner,service.SupplierAccountFilter{Page:1,PageSize:20})
	require.NoError(t,err);require.Len(t,items,1);require.EqualValues(t,1,items[0].Usage.Requests)
	detail,err:=repo.Get(context.Background(),owner,items[0].ID);require.NoError(t,err)
	require.Equal(t,detail.Usage,items[0].Usage)
}
