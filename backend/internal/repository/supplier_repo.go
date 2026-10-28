package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type supplierRepository struct{ db *sql.DB }

func NewSupplierRepository(db *sql.DB) service.SupplierRepository { return &supplierRepository{db: db} }

const supplierProfileColumns = `user_id,status,review_reason,COALESCE(agreement_version,0),agreement_accepted_at,application_fields,payout_frozen,version`

type supplierScanner interface{ Scan(...any) error }

func scanSupplier(row supplierScanner) (*service.SupplierProfile, error) {
	var p service.SupplierProfile
	var fields []byte
	if err := row.Scan(&p.UserID, &p.Status, &p.ReviewReason, &p.AgreementVersion, &p.AgreementAcceptedAt, &fields, &p.PayoutFrozen, &p.Version); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields, &p.ApplicationFields); err != nil {
		return nil, err
	}
	return &p, nil
}
func (r *supplierRepository) GetProfile(ctx context.Context, user int64) (*service.SupplierProfile, error) {
	p, e := scanSupplier(r.db.QueryRowContext(ctx, `SELECT `+supplierProfileColumns+` FROM supplier_profiles WHERE user_id=$1`, user))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return p, e
}
func (r *supplierRepository) Apply(ctx context.Context, user, agreement int64, fields map[string]string) (*service.SupplierProfile, error) {
	raw, e := json.Marshal(fields)
	if e != nil {
		return nil, e
	}
	p, e := scanSupplier(r.db.QueryRowContext(ctx, `INSERT INTO supplier_profiles (user_id,status,agreement_version,agreement_accepted_at,application_fields) VALUES ($1,'pending',$2,NOW(),$3::jsonb) ON CONFLICT (user_id) DO UPDATE SET status='pending',review_reason='',agreement_version=EXCLUDED.agreement_version,agreement_accepted_at=NOW(),application_fields=EXCLUDED.application_fields,version=supplier_profiles.version+1,updated_at=NOW() WHERE supplier_profiles.status IN ('pending','rejected') RETURNING `+supplierProfileColumns, user, agreement, string(raw)))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, apperrors.Forbidden("supplier_not_eligible", "Supplier cannot apply in current status")
	}
	return p, e
}
func (r *supplierRepository) Review(ctx context.Context, user, admin int64, status service.SupplierStatus, reason string) (*service.SupplierProfile, error) {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var previous string
	e = tx.QueryRowContext(ctx, `SELECT status FROM supplier_profiles WHERE user_id=$1 FOR UPDATE`, user).Scan(&previous)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, apperrors.NotFound("supplier_not_found", "Supplier profile not found")
	}
	if e != nil {
		return nil, e
	}
	valid := (previous == "pending" && (status == service.SupplierStatusActive || status == service.SupplierStatusRejected)) || (previous == "active" && status == service.SupplierStatusSuspended) || (previous == "suspended" && status == service.SupplierStatusActive)
	if !valid {
		return nil, apperrors.Conflict("invalid_supplier_transition", "Invalid supplier status transition")
	}
	p, e := scanSupplier(tx.QueryRowContext(ctx, `UPDATE supplier_profiles SET status=$2,review_reason=$3,version=version+1,updated_at=NOW() WHERE user_id=$1 RETURNING `+supplierProfileColumns, user, status, reason))
	if e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO supplier_review_audits (supplier_user_id,admin_user_id,previous_status,new_status,reason) VALUES ($1,$2,$3,$4,$5)`, user, admin, previous, status, reason); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return p, nil
}
func (r *supplierRepository) ListProfiles(ctx context.Context, page, pageSize int) ([]service.SupplierProfile, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM supplier_profiles`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+supplierProfileColumns+` FROM supplier_profiles ORDER BY user_id LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []service.SupplierProfile{}
	for rows.Next() {
		p, err := scanSupplier(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, rows.Err()
}
func (r *supplierRepository) ListAccounts(ctx context.Context, owner int64) ([]service.SupplierAccount, error) {
	rows, e := r.db.QueryContext(ctx, `SELECT id,name,platform,type,status FROM accounts WHERE owner_user_id=$1 AND deleted_at IS NULL ORDER BY id`, owner)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []service.SupplierAccount{}
	for rows.Next() {
		var a service.SupplierAccount
		if e = rows.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (r *supplierRepository) GetAccount(ctx context.Context, owner, id int64) (*service.SupplierAccount, error) {
	var a service.SupplierAccount
	e := r.db.QueryRowContext(ctx, `SELECT id,name,platform,type,status FROM accounts WHERE id=$1 AND owner_user_id=$2 AND deleted_at IS NULL`, id, owner).Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, apperrors.NotFound("account_not_found", "Account not found")
	}
	return &a, e
}
