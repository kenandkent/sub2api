package repository

import (
	"context"
	"database/sql"
	"errors"
)

// CurrentFundingBalance is authoritative for migrated buyers. Their available
// balance changes synchronously when marketplace funds are held or released.
func (r *userRepository) CurrentFundingBalance(ctx context.Context, userID int64) (float64, bool, error) {
	if r == nil || r.sql == nil {
		return 0, false, errors.New("funding balance database unavailable")
	}
	if r.client == nil || r.client.Driver().Dialect() != "postgres" {
		return 0, false, nil
	}
	var balance float64
	err := scanSingleRow(ctx, r.sql, `SELECT u.balance FROM users u JOIN wallet_funding_states f ON f.user_id=u.id WHERE u.id=$1 AND u.deleted_at IS NULL`, []any{userID}, &balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return balance, true, nil
}
