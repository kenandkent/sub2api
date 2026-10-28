package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SharingOfferRepository persists supplier credentials only as authenticated envelopes.
type SharingOfferRepository struct {
	db            *sql.DB
	envelope      *service.CredentialEnvelope
	providerProxy *int64
}

func NewSharingOfferRepository(db *sql.DB, envelope *service.CredentialEnvelope) *SharingOfferRepository {
	return &SharingOfferRepository{db: db, envelope: envelope}
}

// SetProviderProxy supplies the operator-owned OpenAI proxy, never a user input.
func (r *SharingOfferRepository) SetProviderProxy(proxy *int64) {
	r.providerProxy = nil
	if proxy != nil {
		id := *proxy
		r.providerProxy = &id
	}
}

// supplierOAuthIdentity only handles tokens received from the fixed OpenAI
// server-side exchange. ParseIDToken does not verify JWT signatures: the trust
// boundary is the authenticated HTTPS token response, never a client JWT.
func supplierOAuthIdentity(token *openai.TokenResponse) (string, string) {
	claims, err := openai.ParseIDToken(token.IDToken)
	if err != nil || claims.Iss != "https://auth.openai.com" || claims.Exp <= time.Now().Unix() {
		return "", ""
	}
	audience := false
	for _, aud := range claims.Aud {
		if aud == openai.ClientID {
			audience = true
		}
	}
	if !audience {
		return "", ""
	}
	info := claims.GetUserInfo()
	if !validOAuthRoutingID(info.ChatGPTAccountID) {
		return "", ""
	}
	if info.ChatGPTUserID != "" && !validOAuthRoutingID(info.ChatGPTUserID) {
		return "", ""
	}
	return info.ChatGPTAccountID, info.ChatGPTUserID
}

func validOAuthRoutingID(id string) bool {
	if len(id) == 0 || len(id) > 512 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

// SaveSupplierAuthorization couples the account write and durable operation result.
// An unsuccessful or ambiguous exchange must never call this method.
func (r *SharingOfferRepository) SaveSupplierAuthorization(ctx context.Context, owner int64, purpose string, target int64, provider string, token *openai.TokenResponse) (int64, error) {
	operation := service.SupplierAuthorizationOperationID(ctx)
	if r == nil || r.db == nil || r.envelope == nil || owner <= 0 || operation == "" || provider != "openai" || token == nil || strings.TrimSpace(token.AccessToken) == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 31536000 || ((purpose != "create" || target != 0) && (purpose != "reauthorize" || target <= 0)) {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
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
	accountID, userID := supplierOAuthIdentity(token)
	if purpose == "reauthorize" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM marketplace_credential_fingerprints WHERE account_id=$1`, id); err != nil {
			return 0, err
		}
	}
	if accountID != "" {
		fingerprint := sha256.Sum256([]byte(accountID))
		result, e := tx.ExecContext(ctx, `INSERT INTO marketplace_credential_fingerprints(provider,fingerprint,account_id) VALUES('openai:oauth',$1,$2) ON CONFLICT DO NOTHING`, hex.EncodeToString(fingerprint[:]), id)
		if e != nil {
			return 0, e
		}
		count, e := result.RowsAffected()
		if e != nil || count != 1 {
			return 0, service.ErrSupplierAuthorizationFailed
		}
	}
	credentials := map[string]any{"access_token": token.AccessToken, "refresh_token": token.RefreshToken, "id_token": token.IDToken, "token_type": token.TokenType, "scope": token.Scope, "expires_at": time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)}
	if accountID != "" {
		credentials["chatgpt_account_id"] = accountID
		if userID != "" {
			credentials["chatgpt_user_id"] = userID
		}
	}
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
	result, err := tx.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_strip_nulls(jsonb_build_object('base_url',credentials->'base_url','model_mapping',credentials->'model_mapping','openai_capabilities',credentials->'openai_capabilities')) || $1::jsonb,extra=COALESCE(extra,'{}'::jsonb) || '{"openai_responses_supported":true}'::jsonb,proxy_id=$4,updated_at=NOW() WHERE id=$2 AND owner_user_id=$3`, string(payload), id, owner, r.providerProxy)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return 0, service.ErrSupplierAuthorizationFailed
	}
	if purpose == "reauthorize" {
		reviewStatus := "pending_review"
		if accountID == "" {
			reviewStatus = "draft"
		}
		_, err = tx.ExecContext(ctx, `UPDATE sharing_offers SET review_status=$3,enabled=false,version=version+1,revoked_at=NULL,updated_at=NOW() WHERE account_id=$1 AND owner_user_id=$2`, id, owner, reviewStatus)
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
