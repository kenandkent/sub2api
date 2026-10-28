//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFinancialIntegrationDatabaseIsolation(t *testing.T) {
	ctx := context.Background()
	baseline := uuid.NewString() + "@example.test"
	fixture := uuid.NewString() + "@example.test"
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x')`, baseline)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM users WHERE email=$1`, baseline)
		require.NoError(t, err)
	})

	t.Run("committed fixture", func(t *testing.T) {
		isolateCommittedIntegrationTest(t)
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email=$1`, baseline).Scan(&count))
		require.Zero(t, count)
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x')`, fixture)
		require.NoError(t, err)
	})

	var baselineCount, fixtureCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email=$1`, baseline).Scan(&baselineCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email=$1`, fixture).Scan(&fixtureCount))
	require.Equal(t, 1, baselineCount)
	require.Zero(t, fixtureCount)
}
