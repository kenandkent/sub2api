package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

type walletFundingMigrationRepository struct{ db *sql.DB }

func NewWalletFundingMigrationRepository(db *sql.DB) service.WalletFundingMigrationRepository {
	return &walletFundingMigrationRepository{db: db}
}

func (r *walletFundingMigrationRepository) MigrateUser(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, `SELECT marketplace_migrate_funding_user($1)`, userID)
	return err
}

func (r *walletFundingMigrationRepository) GetBalance(ctx context.Context, userID int64) (*service.MarketplaceFundingBalance, error) {
	var balance, frozen, debt, total, reserved, eligible decimal.Decimal
	var version sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
 SELECT u.balance,coalesce(u.frozen_balance,0),coalesce(s.debt,0),s.migration_version,
        coalesce(l.total,0),coalesce(l.reserved,0),coalesce(l.eligible,0)
 FROM users u LEFT JOIN wallet_funding_states s ON s.user_id=u.id
 LEFT JOIN LATERAL (
   SELECT sum(remaining) total,sum(reserved) reserved,
     sum(CASE WHEN kind IN ('cash_paid','platform_subsidy') THEN remaining-reserved ELSE 0 END) eligible
   FROM wallet_funding_lots WHERE user_id=u.id
 ) l ON true WHERE u.id=$1 AND u.deleted_at IS NULL`, userID).
		Scan(&balance, &frozen, &debt, &version, &total, &reserved, &eligible)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	migrated := version.Valid && version.Int64 > 0
	reconciled := migrated && total.Sub(debt).Equal(balance.Add(frozen)) && reserved.Equal(frozen)
	if !reconciled || debt.IsPositive() {
		eligible = decimal.Zero
	}
	eligible = decimal.Min(eligible, decimal.Max(balance, decimal.Zero))
	return &service.MarketplaceFundingBalance{
		Currency: "USD", EligibleAvailable: service.Money{Decimal: eligible},
		Reserved: service.Money{Decimal: frozen}, ExcludedAvailable: service.Money{Decimal: decimal.Max(balance, decimal.Zero).Sub(eligible)},
		Debt: service.Money{Decimal: debt}, Migrated: migrated, Reconciled: reconciled,
	}, nil
}
