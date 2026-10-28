package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type MarketplaceVaultRotationRepository struct {
	db       *sql.DB
	envelope *service.CredentialEnvelope
}

func NewMarketplaceVaultRotationRepository(db *sql.DB, envelope *service.CredentialEnvelope) *MarketplaceVaultRotationRepository {
	return &MarketplaceVaultRotationRepository{db: db, envelope: envelope}
}

func (r *MarketplaceVaultRotationRepository) Rotate(ctx context.Context, actor int64, operation, reason string, limit int) (*service.VaultRotationResult, error) {
	if r == nil || r.db == nil || r.envelope == nil {
		return nil, service.ErrVaultRotationUnavailable
	}
	processed, remaining, completed, err := NewSupplierPayoutRepository(r.db).RotateCredentialVault(ctx, actor, operation, reason, limit, r.envelope)
	if err != nil {
		return nil, err
	}
	return &service.VaultRotationResult{OperationID: operation, Processed: processed, Remaining: remaining, Completed: completed, TargetKeyID: r.envelope.CurrentKeyID()}, nil
}

type vaultEnvelopeRow struct {
	id, owner int64
	provider  string
	raw       []byte
}

// RotateCredentialVault rotates a bounded, atomic batch; every replay observes
// the original recorded result. It never returns or logs credential content.
func (r *SupplierPayoutRepository) RotateCredentialVault(ctx context.Context, actor int64, operation, reason string, limit int, envelope *service.CredentialEnvelope) (int, int, bool, error) {
	target := envelope.CurrentKeyID()
	if r == nil || r.db == nil || target == "" || actor <= 0 || strings.TrimSpace(operation) != operation || operation == "" || len(operation) > 255 || strings.TrimSpace(reason) != reason || reason == "" || len(reason) > 4096 || limit < 1 || limit > 200 {
		return 0, 0, false, service.ErrVaultRotationInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('marketplace:vault-rotation',0))`); err != nil {
		return 0, 0, false, err
	}
	if !lockPayoutAdmin(ctx, tx, actor) {
		return 0, 0, false, service.ErrVaultRotationInvalid
	}
	var storedActor int64
	var storedReason, storedTarget, status string
	var storedLimit, processed, remaining int
	var complete bool
	err = tx.QueryRowContext(ctx, `SELECT actor_user_id,reason,target_key_id,batch_limit,status,COALESCE(processed,0),COALESCE(remaining,0),COALESCE(completed,false) FROM marketplace_vault_rotation_operations WHERE operation_id=$1 FOR UPDATE`, operation).Scan(&storedActor, &storedReason, &storedTarget, &storedLimit, &status, &processed, &remaining, &complete)
	if err == nil {
		if storedActor != actor || storedReason != reason || storedTarget != target || storedLimit != limit || status != "completed" {
			return 0, 0, false, service.ErrVaultRotationConflict
		}
		if err = tx.Commit(); err != nil {
			return 0, 0, false, err
		}
		return processed, remaining, complete, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, err
	}
	// Serialize concurrent credential inserts and updates with the completion
	// count. Every server must already use the same current key before rotation.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE accounts, supplier_payout_destinations IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return 0, 0, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO marketplace_vault_rotation_operations(operation_id,actor_user_id,reason,target_key_id,batch_limit,status) VALUES($1,$2,$3,$4,$5,'processing')`, operation, actor, reason, target, limit); err != nil {
		return 0, 0, false, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT set_config('marketplace.vault_rotation_operation',$1,true)`, operation); err != nil {
		return 0, 0, false, err
	}
	accounts, err := collectVaultRows(ctx, tx, `SELECT id,owner_user_id,platform,credentials->'_supplier_envelope' FROM accounts WHERE owner_user_id IS NOT NULL AND credentials ? '_supplier_envelope' AND COALESCE(credentials->'_supplier_envelope'->>'key_id','')<>$1 ORDER BY id LIMIT $2 FOR UPDATE`, target, limit)
	if err != nil {
		return 0, 0, false, err
	}
	for _, row := range accounts {
		next, e := rotateVaultEnvelope(envelope, row)
		if e != nil {
			return 0, 0, false, service.ErrVaultRotationUnavailable
		}
		result, e := tx.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{_supplier_envelope}',$1::jsonb),updated_at=NOW() WHERE id=$2 AND owner_user_id=$3 AND credentials->'_supplier_envelope'=$4::jsonb`, string(next), row.id, row.owner, string(row.raw))
		if e != nil {
			return 0, 0, false, e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return 0, 0, false, service.ErrVaultRotationConflict
		}
		processed++
	}
	if processed < limit {
		destinations, e := collectVaultRows(ctx, tx, `SELECT id,supplier_id,'payout',credential FROM supplier_payout_destinations WHERE COALESCE(credential->>'key_id','')<>$1 ORDER BY id LIMIT $2 FOR UPDATE`, target, limit-processed)
		if e != nil {
			return 0, 0, false, e
		}
		for _, row := range destinations {
			next, e := rotateVaultEnvelope(envelope, row)
			if e != nil {
				return 0, 0, false, service.ErrVaultRotationUnavailable
			}
			result, e := tx.ExecContext(ctx, `UPDATE supplier_payout_destinations SET credential=$1::jsonb WHERE id=$2 AND supplier_id=$3 AND credential=$4::jsonb`, string(next), row.id, row.owner, string(row.raw))
			if e != nil {
				return 0, 0, false, e
			}
			if n, e := result.RowsAffected(); e != nil || n != 1 {
				return 0, 0, false, service.ErrVaultRotationConflict
			}
			processed++
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM accounts WHERE owner_user_id IS NOT NULL AND credentials ? '_supplier_envelope' AND COALESCE(credentials->'_supplier_envelope'->>'key_id','')<>$1) + (SELECT count(*) FROM supplier_payout_destinations WHERE COALESCE(credential->>'key_id','')<>$1)`, target).Scan(&remaining)
	if err != nil {
		return 0, 0, false, err
	}
	complete = remaining == 0
	if _, err = tx.ExecContext(ctx, `UPDATE marketplace_vault_rotation_operations SET status='completed',processed=$2,remaining=$3,completed=$4 WHERE operation_id=$1`, operation, processed, remaining, complete); err != nil {
		return 0, 0, false, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, false, err
	}
	return processed, remaining, complete, nil
}

func collectVaultRows(ctx context.Context, tx *sql.Tx, query, target string, limit int) ([]vaultEnvelopeRow, error) {
	rows, err := tx.QueryContext(ctx, query, target, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []vaultEnvelopeRow
	for rows.Next() {
		var row vaultEnvelopeRow
		if err = rows.Scan(&row.id, &row.owner, &row.provider, &row.raw); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func rotateVaultEnvelope(envelope *service.CredentialEnvelope, row vaultEnvelopeRow) ([]byte, error) {
	var enc service.EncryptedCredential
	if err := json.Unmarshal(row.raw, &enc); err != nil {
		return nil, err
	}
	plain, err := envelope.Open(row.provider, row.owner, row.id, enc)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	next, err := envelope.Seal(row.provider, row.owner, row.id, plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(next)
}
