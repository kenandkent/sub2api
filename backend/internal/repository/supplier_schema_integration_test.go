//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupplierSchemaOwnerAndOfferConstraints(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	tx := testTx(t)
	var owner, other, account int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'hash') RETURNING id`, uniqueTestValue(t, "owner")+"@example.com").Scan(&owner))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'hash') RETURNING id`, uniqueTestValue(t, "other")+"@example.com").Scan(&other))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts (name,platform,type) VALUES ('legacy','openai','oauth') RETURNING id`).Scan(&account))
	var legacyOwner sql.NullInt64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT owner_user_id FROM accounts WHERE id=$1`, account).Scan(&legacyOwner))
	require.False(t, legacyOwner.Valid)
	_, err := tx.ExecContext(ctx, `SAVEPOINT ownership_test`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET owner_user_id=$1 WHERE id=$2`, owner, account)
	require.Error(t, err, "a legacy account must never be converted to supplier ownership")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT ownership_test`)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,owner_user_id) VALUES('supplier','openai','oauth',$1) RETURNING id`, owner).Scan(&account))
	_, err = tx.ExecContext(ctx, `INSERT INTO sharing_offers (account_id,owner_user_id) VALUES ($1,$2)`, account, owner)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, query string
		args        []any
	}{
		{"duplicate offer", `INSERT INTO sharing_offers (account_id,owner_user_id) VALUES ($1,$2)`, []any{account, owner}},
		{"wrong offer owner", `UPDATE sharing_offers SET owner_user_id=$2 WHERE account_id=$1`, []any{account, other}},
		{"unknown account owner", `INSERT INTO accounts (name,platform,type,owner_user_id) VALUES ('bad','openai','oauth',-1)`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err = tx.ExecContext(ctx, `SAVEPOINT constraint_test`)
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, tc.query, tc.args...)
			require.Error(t, err)
			_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT constraint_test`)
			require.NoError(t, err)
		})
	}
}

func TestSupplierSchemaPublishedVersionsImmutable(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	tx := testTx(t)
	var publisher int64
	var version int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT greatest(coalesce((SELECT max(version) FROM marketplace_policies),0),coalesce((SELECT max(version) FROM marketplace_price_versions),0))+1`).Scan(&version))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'hash') RETURNING id`, uniqueTestValue(t, "publisher")+"@example.com").Scan(&publisher))
	for _, tc := range []struct{ name, insert, update, delete string }{
		{"policy", `INSERT INTO marketplace_policies (version,fee_bps,hold_hours,max_supplier_concurrency,minimum_payout_usd,published_by,published_at) VALUES ($2,100,0,1,0,$1,now())`, `UPDATE marketplace_policies SET fee_bps=200 WHERE version=$1`, `DELETE FROM marketplace_policies WHERE version=$1`},
		{"price", `INSERT INTO marketplace_price_versions (version,published_by,published_at) VALUES ($2,$1,now())`, `UPDATE marketplace_price_versions SET rules='{"changed":true}' WHERE version=$1`, `DELETE FROM marketplace_price_versions WHERE version=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tx.ExecContext(ctx, tc.insert, publisher, version)
			require.NoError(t, err)
			for _, query := range []string{tc.update, tc.delete} {
				_, err = tx.ExecContext(ctx, `SAVEPOINT immutable_test`)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, query, version)
				require.Error(t, err)
				_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT immutable_test`)
				require.NoError(t, err)
			}
		})
	}
}
