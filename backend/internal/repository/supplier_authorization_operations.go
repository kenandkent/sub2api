package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SupplierAuthorizationOperations stores only hashes of callback secrets.
type SupplierAuthorizationOperations struct{ db *sql.DB }

func NewSupplierAuthorizationOperationStore(db *sql.DB) *SupplierAuthorizationOperations {
	return &SupplierAuthorizationOperations{db: db}
}

type supplierAuthorizationMetadata struct {
	Provider       string `json:"provider"`
	Purpose        string `json:"purpose"`
	AccountID      int64  `json:"account_id"`
	StateDigest    string `json:"state_digest"`
	CallbackDigest string `json:"callback_digest,omitempty"`
}

func (r *SupplierAuthorizationOperations) Create(ctx context.Context, op service.SupplierAuthorizationOperation) error {
	m := supplierAuthorizationMetadata{Provider: op.Provider, Purpose: op.Purpose, AccountID: op.Target, StateDigest: op.StateDigest}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	_, err = r.db.ExecContext(ctx, `INSERT INTO supplier_operations (user_id, operation_key, kind, request_digest, status, metadata) VALUES ($1,$2,'authorization',$3,'pending',$4::jsonb)`, op.UserID, op.ID, hex.EncodeToString(digest[:]), string(data))
	return err
}
func (r *SupplierAuthorizationOperations) Get(ctx context.Context, user int64, id string) (*service.SupplierAuthorizationOperation, error) {
	op := &service.SupplierAuthorizationOperation{UserID: user, ID: id}
	var metadata []byte
	var account sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT status, account_id, metadata FROM supplier_operations WHERE user_id=$1 AND operation_key=$2 AND kind='authorization'`, user, id).Scan(&op.Status, &account, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m supplierAuthorizationMetadata
	if err = json.Unmarshal(metadata, &m); err != nil {
		return nil, err
	}
	op.Provider, op.Purpose, op.Target, op.StateDigest, op.CallbackDigest = m.Provider, m.Purpose, m.AccountID, m.StateDigest, m.CallbackDigest
	if account.Valid {
		op.AccountID = &account.Int64
	}
	return op, nil
}
func (r *SupplierAuthorizationOperations) Claim(ctx context.Context, user int64, id, digest string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE supplier_operations SET status='processing', metadata=jsonb_set(metadata,'{callback_digest}',to_jsonb($3::text)), updated_at=now() WHERE user_id=$1 AND operation_key=$2 AND kind='authorization' AND status='pending'`, user, id, digest)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (r *SupplierAuthorizationOperations) Fail(ctx context.Context, user int64, id, expected, code string) error {
	status := "failed"
	if code == "write_uncertain" {
		status = "needs_review"
	}
	_, err := r.db.ExecContext(ctx, `UPDATE supplier_operations SET status=$4,error_code=$5,updated_at=now() WHERE user_id=$1 AND operation_key=$2 AND kind='authorization' AND status=$3`, user, id, expected, status, code)
	return err
}
