//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func marketplaceUpgradeDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("market_upgrade_%d", atomic.AddUint64(&integrationDatabaseSeq, 1))
	_, err := integrationAdminDB.ExecContext(context.Background(), `CREATE DATABASE `+name)
	require.NoError(t, err)
	dsn, err := url.Parse(integrationDSN)
	require.NoError(t, err)
	dsn.Path = "/" + name
	db, err := openSQLWithRetry(context.Background(), dsn.String(), 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		_, err := integrationAdminDB.ExecContext(context.Background(), `DROP DATABASE `+name)
		require.NoError(t, err)
	})
	_, err = db.Exec(`CREATE TABLE users(id bigint PRIMARY KEY); CREATE TABLE accounts(id bigint PRIMARY KEY); INSERT INTO accounts(id) VALUES(1),(2); CREATE TABLE marketplace_requests(id uuid PRIMARY KEY); CREATE TABLE marketplace_captures(id bigint PRIMARY KEY); CREATE TABLE api_keys(id bigint PRIMARY KEY); CREATE TABLE usage_logs(id bigint PRIMARY KEY); INSERT INTO usage_logs(id) VALUES(1),(2)`)
	require.NoError(t, err)
	return db
}

func marketplaceUpgradeFS(t *testing.T, names ...string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	for _, name := range names {
		content, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		out[name] = &fstest.MapFile{Data: content}
	}
	return out
}

func TestMarketplaceUpgradeFreshAndPrototypeChecksums(t *testing.T) {
	for _, prototype := range []bool{false, true} {
		t.Run(fmt.Sprint("prototype=", prototype), func(t *testing.T) {
			db := marketplaceUpgradeDB(t)
			ctx := context.Background()
			if !prototype {
				// A failed prior CREATE INDEX CONCURRENTLY must not poison a retry.
				_, err := db.Exec(`ALTER TABLE accounts ADD COLUMN owner_user_id bigint`)
				require.NoError(t, err)
				_, err = db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY accounts_id_owner_user_id_key ON accounts ((coalesce(owner_user_id,0)))`)
				require.Error(t, err)
			}
			if prototype {
				old := fstest.MapFS{}
				for _, name := range []string{"250_marketplace_identity.sql", "265_marketplace_projection.sql"} {
					content, err := os.ReadFile("testdata/marketplace_upgrade/" + name)
					require.NoError(t, err)
					old[name] = &fstest.MapFile{Data: content}
				}
				require.NoError(t, applyMigrationsFS(ctx, db, old))
			}
			updated := marketplaceUpgradeFS(t, "249_z_marketplace_owner_prepare.sql", "249_zz_marketplace_owner_indexes_notx.sql", "250_marketplace_identity.sql", "250_z_marketplace_owner_validate.sql", "265_marketplace_projection.sql", "265_z_marketplace_receipt_index_notx.sql", "265_zz_marketplace_receipt_validate.sql")
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- applyMigrationsFS(ctx, db, updated) }()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			require.NoError(t, applyMigrationsFS(ctx, db, updated))
			var valid int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conname IN ('accounts_owner_user_id_fkey','usage_logs_marketplace_receipt_id_fkey') AND convalidated`).Scan(&valid))
			require.Equal(t, 2, valid)
			var indexes int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relname IN ('accounts_owner_user_id_idx','accounts_id_owner_user_id_key','usage_logs_marketplace_receipt_unique') AND i.indisvalid`).Scan(&indexes))
			require.Equal(t, 3, indexes)
			if prototype {
				var ownerChecksum, projectionChecksum string
				require.NoError(t, db.QueryRow(`SELECT checksum FROM schema_migrations WHERE filename='250_marketplace_identity.sql'`).Scan(&ownerChecksum))
				require.NoError(t, db.QueryRow(`SELECT checksum FROM schema_migrations WHERE filename='265_marketplace_projection.sql'`).Scan(&projectionChecksum))
				require.Equal(t, "bd0d4c07d48ebf7b3d7d97e702245e6610f484921f2290ed3d6e19f49f8a9bf8", ownerChecksum)
				require.Equal(t, "4f306df8f38ded0718413dc94cff6c01c86dd69f527fb6179b9294250bbaeab2", projectionChecksum)
			}
			// Simulate interrupted/invalid concurrent index creation and rerun the
			// actual nontransactional migration through the production runner.
			_, err := db.Exec(`DROP INDEX CONCURRENTLY usage_logs_marketplace_receipt_unique`)
			require.NoError(t, err)
			_, err = db.Exec(`DELETE FROM schema_migrations WHERE filename='265_z_marketplace_receipt_index_notx.sql'`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY usage_logs_marketplace_receipt_unique ON usage_logs ((1))`)
			require.Error(t, err)
			require.NoError(t, applyMigrationsFS(ctx, db, updated))
			if !prototype {
				_, err = db.Exec(`ALTER TABLE marketplace_captures ADD COLUMN matures_at timestamptz, ADD COLUMN status text, ADD COLUMN owner_user_id bigint, ADD COLUMN ledger_transaction_id bigint; INSERT INTO marketplace_captures(id,status) VALUES(1,'pending'),(2,'pending')`)
				require.NoError(t, err)
				_, err = db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY marketplace_captures_pending_maturity_idx ON marketplace_captures ((1))`)
				require.Error(t, err)
				require.NoError(t, applyMigrationsFS(ctx, db, marketplaceUpgradeFS(t, "283_marketplace_indexes_notx.sql")))
			}
			if prototype {
				_, err := db.Exec(`UPDATE schema_migrations SET checksum='unexpected' WHERE filename='250_marketplace_identity.sql'`)
				require.NoError(t, err)
				require.ErrorContains(t, applyMigrationsFS(ctx, db, updated), "checksum mismatch")
			}
		})
	}
}
