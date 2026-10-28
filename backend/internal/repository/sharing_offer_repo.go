package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SharingOfferRepository persists supplier credentials only as authenticated envelopes.
type SharingOfferRepository struct {
	db       *sql.DB
	envelope *service.CredentialEnvelope
}

func NewSharingOfferRepository(db *sql.DB, envelope *service.CredentialEnvelope) *SharingOfferRepository {
	return &SharingOfferRepository{db: db, envelope: envelope}
}

// SaveSupplierAuthorization couples the account write and durable operation result.
// An unsuccessful or ambiguous exchange must never call this method.
func (r *SharingOfferRepository) SaveSupplierAuthorization(ctx context.Context, owner int64, purpose string, target int64, provider string, token *openai.TokenResponse) (int64, error) {
	operation := service.SupplierAuthorizationOperationID(ctx)
	if r == nil || r.db == nil || r.envelope == nil || owner <= 0 || operation == "" || provider != "openai" || token == nil || strings.TrimSpace(token.AccessToken) == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 31536000 || !((purpose == "create" && target == 0) || (purpose == "reauthorize" && target > 0)) {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT status = 'active' FROM supplier_profiles WHERE user_id = $1 FOR SHARE`, owner).Scan(&active)
	if err != nil || !active {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT metadata FROM supplier_operations WHERE user_id=$1 AND operation_key=$2 AND kind='authorization' AND status='processing' FOR UPDATE`, owner, operation).Scan(&raw)
	if err != nil {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	var meta struct {
		Provider  string `json:"provider"`
		Purpose   string `json:"purpose"`
		AccountID int64  `json:"account_id"`
	}
	if json.Unmarshal(raw, &meta) != nil || meta.Provider != provider || meta.Purpose != purpose || meta.AccountID != target {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	id := target
	if purpose == "create" {
		err = tx.QueryRowContext(ctx, `INSERT INTO accounts (owner_user_id,name,platform,type,credentials,concurrency) VALUES ($1,'OpenAI OAuth',$2,'oauth','{}'::jsonb,1) RETURNING id`, owner, provider).Scan(&id)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sharing_offers (account_id,owner_user_id,review_status,enabled,allowed_models) VALUES ($1,$2,'draft',false,'[]'::jsonb)`, id, owner)
		if err != nil {
			return 0, err
		}
	} else {
		var locked int64
		err = tx.QueryRowContext(ctx, `SELECT a.id FROM accounts a JOIN sharing_offers o ON o.account_id=a.id AND o.owner_user_id=a.owner_user_id WHERE a.id=$1 AND a.owner_user_id=$2 AND a.platform=$3 AND a.type='oauth' AND a.parent_account_id IS NULL AND a.deleted_at IS NULL FOR UPDATE OF a,o`, id, owner, provider).Scan(&locked)
		if err != nil {
			return 0, service.ErrSupplierAuthorizationFailed
		}
	}
	credentials := map[string]any{"access_token": token.AccessToken, "refresh_token": token.RefreshToken, "id_token": token.IDToken, "token_type": token.TokenType, "scope": token.Scope, "expires_at": time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)}
	plain, err := json.Marshal(credentials)
	if err != nil {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	sealed, err := r.envelope.Seal(provider, owner, id, plain)
	if err != nil {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	payload, err := json.Marshal(map[string]any{"_supplier_envelope": sealed})
	if err != nil {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	result, err := tx.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_strip_nulls(jsonb_build_object('base_url',credentials->'base_url','model_mapping',credentials->'model_mapping','openai_capabilities',credentials->'openai_capabilities')) || $1::jsonb,updated_at=NOW() WHERE id=$2 AND owner_user_id=$3`, string(payload), id, owner)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	if purpose == "reauthorize" {
		_, err = tx.ExecContext(ctx, `UPDATE sharing_offers SET review_status='pending_review',enabled=false,version=version+1,revoked_at=NULL,updated_at=NOW() WHERE account_id=$1 AND owner_user_id=$2`, id, owner)
		if err != nil {
			return 0, err
		}
	}
	result, err = tx.ExecContext(ctx, `UPDATE supplier_operations SET status='completed',account_id=$3,updated_at=NOW() WHERE user_id=$1 AND operation_key=$2 AND kind='authorization' AND status='processing'`, owner, operation, id)
	if err != nil {
		return 0, err
	}
	count, err = result.RowsAffected()
	if err != nil || count != 1 {
		return 0, errors.New("authorization operation completion failed")
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
