package repository

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type SupplierRuntimeCredentialRepository struct{ db *sql.DB }

func NewSupplierRuntimeCredentialRepository(db *sql.DB) *SupplierRuntimeCredentialRepository {
	return &SupplierRuntimeCredentialRepository{db: db}
}
func (r *SupplierRuntimeCredentialRepository) LoadSupplierCredential(ctx context.Context, account, owner int64) (service.EncryptedCredential, error) {
	var result service.EncryptedCredential
	if r == nil || r.db == nil || account <= 0 || owner <= 0 {
		return result, service.ErrSupplierRuntimeUnavailable
	}
	var raw []byte
	if err := r.db.QueryRowContext(ctx, `SELECT credentials->'_supplier_envelope' FROM accounts WHERE id=$1 AND owner_user_id=$2 AND platform='openai' AND parent_account_id IS NULL AND deleted_at IS NULL`, account, owner).Scan(&raw); err != nil {
		return result, service.ErrSupplierRuntimeUnavailable
	}
	if json.Unmarshal(raw, &result) != nil || result.Version != 1 || result.KeyID == "" || len(result.Nonce) == 0 || len(result.Ciphertext) == 0 {
		return service.EncryptedCredential{}, service.ErrSupplierRuntimeUnavailable
	}
	return result, nil
}
func (r *SupplierRuntimeCredentialRepository) CompareAndSwapSupplierCredential(ctx context.Context, account, owner int64, old, next service.EncryptedCredential) (bool, error) {
	if r == nil || r.db == nil || account <= 0 || owner <= 0 || old.Version != 1 || next.Version != 1 {
		return false, service.ErrSupplierRuntimeUnavailable
	}
	oldJSON, err := json.Marshal(old)
	if err != nil {
		return false, service.ErrSupplierRuntimeUnavailable
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return false, service.ErrSupplierRuntimeUnavailable
	}
	result, err := r.db.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_strip_nulls(jsonb_build_object('base_url',credentials->'base_url','model_mapping',credentials->'model_mapping','openai_capabilities',credentials->'openai_capabilities')) || jsonb_build_object('_supplier_envelope',$1::jsonb),updated_at=NOW() WHERE id=$2 AND owner_user_id=$3 AND platform='openai' AND parent_account_id IS NULL AND deleted_at IS NULL AND credentials->'_supplier_envelope'=$4::jsonb`, string(nextJSON), account, owner, string(oldJSON))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
