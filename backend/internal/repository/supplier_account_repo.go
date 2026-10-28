package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type SupplierAccountRepository struct {
	db       *sql.DB
	envelope *service.CredentialEnvelope
}

func NewSupplierAccountRepository(db *sql.DB, envelope *service.CredentialEnvelope) *SupplierAccountRepository {
	return &SupplierAccountRepository{db: db, envelope: envelope}
}
func accountNotFound() error { return apperrors.NotFound("account_not_found", "Account not found") }
func versionConflict() error {
	return apperrors.Conflict("version_conflict", "Account version changed")
}
func activeSupplier(ctx context.Context, tx *sql.Tx, user int64) error {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT status='active' FROM supplier_profiles WHERE user_id=$1 FOR SHARE`, user).Scan(&active)
	if err != nil || !active {
		return apperrors.Forbidden("supplier_not_active", "Supplier cannot manage offers")
	}
	return nil
}
func scanAccount(row interface{ Scan(...any) error }) (*service.SupplierAccountDetail, error) {
	var a service.SupplierAccountDetail
	var models []byte
	var hasCred bool
	err := row.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &a.Status, &a.Concurrency, &hasCred, &a.Offer.ID, &a.Offer.ReviewStatus, &a.Offer.Enabled, &models, &a.Offer.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, accountNotFound()
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(models, &a.Offer.AllowedModels); err != nil {
		return nil, err
	}
	a.CredentialStatus = "missing"
	if hasCred {
		a.CredentialStatus = "configured"
	}
	return &a, nil
}

const supplierAccountSelect = `SELECT a.id,a.name,a.platform,a.type,a.status,a.concurrency,(a.credentials ? '_supplier_envelope'),o.id,o.review_status,o.enabled,o.allowed_models,o.version FROM accounts a JOIN sharing_offers o ON o.account_id=a.id AND o.owner_user_id=a.owner_user_id WHERE a.id=$1 AND a.owner_user_id=$2 AND a.deleted_at IS NULL AND a.parent_account_id IS NULL`

func (r *SupplierAccountRepository) Get(ctx context.Context, user, id int64) (*service.SupplierAccountDetail, error) {
	a, err := scanAccount(r.db.QueryRowContext(ctx, supplierAccountSelect, id, user))
	if err != nil {
		return nil, err
	}
	a.Reviews = []service.SupplierAccountReview{}
	rows, err := r.db.QueryContext(ctx, `SELECT new_status,reason,created_at FROM sharing_offer_review_audits WHERE offer_id=$1 ORDER BY id DESC LIMIT 100`, a.Offer.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var review service.SupplierAccountReview
		if err = rows.Scan(&review.Status, &review.Reason, &review.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		a.Reviews = append(a.Reviews, review)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	err = r.db.QueryRowContext(ctx, usageQuery+` WHERE c.owner_user_id=$1 AND a.account_id=$2`, user, id).Scan(&a.Usage.Requests, &a.Usage.InputTokens, &a.Usage.OutputTokens, &a.Usage.ConsumerCharge, &a.Usage.SupplierNet)
	if err != nil {
		return nil, err
	}
	return a, nil
}
func (r *SupplierAccountRepository) Operation(ctx context.Context, user int64, key string) (*service.SupplierOperation, error) {
	var op service.SupplierOperation
	err := r.db.QueryRowContext(ctx, `SELECT operation_key,kind,status,account_id,error_code FROM supplier_operations WHERE user_id=$1 AND operation_key=$2`, user, key).Scan(&op.Key, &op.Kind, &op.Status, &op.AccountID, &op.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.NotFound("operation_not_found", "Operation not found")
	}
	return &op, err
}
func (r *SupplierAccountRepository) Create(ctx context.Context, user int64, key, digest string, in service.SupplierAccountCreate, p config.MarketplaceProviderConfig) (*service.SupplierOperation, error) {
	if r == nil || r.db == nil || r.envelope == nil {
		return nil, service.ErrCredentialEnvelope
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = activeSupplier(ctx, tx, user); err != nil {
		return nil, err
	}
	var existingDigest string
	var op service.SupplierOperation
	err = tx.QueryRowContext(ctx, `INSERT INTO supplier_operations(user_id,operation_key,kind,request_digest,status) VALUES($1,$2,'account_create',$3,'processing') ON CONFLICT (user_id,operation_key) DO NOTHING RETURNING operation_key,kind,status,account_id,error_code`, user, key, digest).Scan(&op.Key, &op.Kind, &op.Status, &op.AccountID, &op.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT operation_key,kind,status,account_id,error_code,request_digest FROM supplier_operations WHERE user_id=$1 AND operation_key=$2`, user, key).Scan(&op.Key, &op.Kind, &op.Status, &op.AccountID, &op.ErrorCode, &existingDigest)
		if err != nil {
			return nil, err
		}
		if op.Kind != "account_create" || existingDigest != digest {
			return nil, apperrors.Conflict("idempotency_conflict", "Idempotency key reused")
		}
		return &op, nil
	}
	if err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO accounts(owner_user_id,name,platform,type,credentials,concurrency,proxy_id) VALUES($1,$2,$3,$4,'{}'::jsonb,$5,$6) RETURNING id`, user, in.Name, in.Platform, in.Type, in.Concurrency, p.ProxyID).Scan(&id)
	if err != nil {
		return nil, err
	}
	payloadMap := map[string]any{"base_url": p.BaseURL}
	if in.APIKey != "" {
		raw, marshalErr := json.Marshal(map[string]string{"api_key": in.APIKey})
		if marshalErr != nil {
			return nil, marshalErr
		}
		sealed, sealErr := r.envelope.Seal(in.Platform, user, id, raw)
		if sealErr != nil {
			return nil, sealErr
		}
		payloadMap["_supplier_envelope"] = sealed
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET credentials=$1::jsonb WHERE id=$2 AND owner_user_id=$3`, string(payload), id, user)
	if err != nil {
		return nil, err
	}
	if in.APIKey != "" {
		fingerprint := sha256.Sum256([]byte(in.APIKey))
		res, err := tx.ExecContext(ctx, `INSERT INTO marketplace_credential_fingerprints(provider,fingerprint,account_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, in.Platform, hex.EncodeToString(fingerprint[:]), id)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, apperrors.Conflict("duplicate_credential", "Credential already registered")
		}
	}

	models, err := json.Marshal(in.AllowedModels)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sharing_offers(account_id,owner_user_id,allowed_models) VALUES($1,$2,$3::jsonb)`, id, user, string(models))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE supplier_operations SET status='completed',account_id=$3,updated_at=NOW() WHERE user_id=$1 AND operation_key=$2`, user, key, id)
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	op.Status = "completed"
	op.AccountID = &id
	return &op, nil
}
func (r *SupplierAccountRepository) Patch(ctx context.Context, user, id int64, in service.SupplierAccountPatch) (*service.SupplierAccountDetail, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = activeSupplier(ctx, tx, user); err != nil {
		return nil, err
	}
	a, err := scanAccount(tx.QueryRowContext(ctx, supplierAccountSelect+` FOR UPDATE OF a,o`, id, user))
	if err != nil {
		return nil, err
	}
	if a.Offer.Version != in.ExpectedVersion {
		return nil, versionConflict()
	}
	if in.Name != nil {
		a.Name = *in.Name
	}
	if in.Concurrency != nil {
		a.Concurrency = *in.Concurrency
	}
	review := a.Offer.ReviewStatus
	enabled := a.Offer.Enabled
	if in.AllowedModels != nil {
		old := map[string]bool{}
		for _, m := range a.Offer.AllowedModels {
			old[m] = true
		}
		for _, m := range *in.AllowedModels {
			if !old[m] && review == "approved" {
				review = "pending_review"
				enabled = false
			}
		}
		a.Offer.AllowedModels = *in.AllowedModels
	}
	models, err := json.Marshal(a.Offer.AllowedModels)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET name=$1,concurrency=$2,updated_at=NOW() WHERE id=$3 AND owner_user_id=$4`, a.Name, a.Concurrency, id, user)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE sharing_offers SET allowed_models=$1::jsonb,review_status=$2,enabled=$3,version=version+1,updated_at=NOW() WHERE account_id=$4 AND owner_user_id=$5`, string(models), review, enabled, id, user)
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.Get(ctx, user, id)
}
func (r *SupplierAccountRepository) Transition(ctx context.Context, user, id, version int64, action string) (*service.SupplierAccountDetail, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = activeSupplier(ctx, tx, user); err != nil {
		return nil, err
	}
	a, err := scanAccount(tx.QueryRowContext(ctx, supplierAccountSelect+` FOR UPDATE OF a,o`, id, user))
	if err != nil {
		return nil, err
	}
	if a.Offer.Version != version {
		return nil, versionConflict()
	}
	status, enabled := a.Offer.ReviewStatus, a.Offer.Enabled
	switch action {
	case "submit":
		if status != "draft" && status != "rejected" {
			return nil, apperrors.Conflict("invalid_transition", "Invalid offer transition")
		}
		status = "pending_review"
		enabled = false
	case "pause":
		if status != "approved" || !enabled {
			return nil, apperrors.Conflict("invalid_transition", "Invalid offer transition")
		}
		enabled = false
	case "resume":
		if status != "approved" || enabled {
			return nil, apperrors.Conflict("invalid_transition", "Invalid offer transition")
		}
		enabled = true
	case "revoke":
		if status == "revoked" {
			return nil, apperrors.Conflict("invalid_transition", "Invalid offer transition")
		}
		status = "revoked"
		enabled = false
	default:
		return nil, apperrors.BadRequest("invalid_action", "Invalid action")
	}
	_, err = tx.ExecContext(ctx, `UPDATE sharing_offers SET review_status=$1,enabled=$2,version=version+1,revoked_at=CASE WHEN $1::varchar='revoked' THEN NOW() ELSE revoked_at END,updated_at=NOW() WHERE account_id=$3 AND owner_user_id=$4`, status, enabled, id, user)
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.Get(ctx, user, id)
}

func (r *SupplierAccountRepository) List(ctx context.Context, user int64, f service.SupplierAccountFilter) ([]service.SupplierAccountDetail, int64, error) {
	const from = ` FROM accounts a JOIN sharing_offers o ON o.account_id=a.id AND o.owner_user_id=a.owner_user_id WHERE a.owner_user_id=$1 AND a.deleted_at IS NULL AND a.parent_account_id IS NULL AND ($2='' OR a.name ILIKE '%' || $2 || '%') AND ($3='' OR a.platform=$3) AND ($4='' OR o.review_status=$4) AND ($5='' OR o.allowed_models ? $5)`
	args := []any{user, f.Query, f.Platform, f.Status, f.Model}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := r.db.QueryContext(ctx, `SELECT a.id,a.name,a.platform,a.type,a.status,a.concurrency,(a.credentials ? '_supplier_envelope'),o.id,o.review_status,o.enabled,o.allowed_models,o.version`+from+` ORDER BY a.id DESC LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []service.SupplierAccountDetail{}
	for rows.Next() {
		a, e := scanAccount(rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, *a)
	}
	return out, total, rows.Err()
}

func (r *SupplierAccountRepository) GetForReview(ctx context.Context, id int64) (*service.SupplierAccountDetail, error) {
	var owner int64
	if err := r.db.QueryRowContext(ctx, `SELECT owner_user_id FROM sharing_offers WHERE id=$1`, id).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return nil, accountNotFound()
	} else if err != nil {
		return nil, err
	}
	var account int64
	if err := r.db.QueryRowContext(ctx, `SELECT account_id FROM sharing_offers WHERE id=$1`, id).Scan(&account); err != nil {
		return nil, err
	}
	return r.Get(ctx, owner, account)
}
func (r *SupplierAccountRepository) Review(ctx context.Context, admin, id, version int64, status, reason string, groups []int64) (*service.SupplierAccountDetail, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var owner, account, current int64
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT owner_user_id,account_id,version,review_status FROM sharing_offers WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &account, &current, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, accountNotFound()
	}
	if err != nil {
		return nil, err
	}
	if current != version {
		return nil, versionConflict()
	}
	if previous != "pending_review" {
		return nil, apperrors.Conflict("invalid_transition", "Offer is not awaiting review")
	}
	if status == "approved" {
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts a JOIN supplier_profiles p ON p.user_id=a.owner_user_id JOIN marketplace_credential_fingerprints f ON f.account_id=a.id WHERE a.id=$1 AND a.owner_user_id=$2 AND a.parent_account_id IS NULL AND a.deleted_at IS NULL AND p.status='active')`, account, owner).Scan(&valid)
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, apperrors.Conflict("identity_unverified", "Credential identity requires manual verification")
		}
		if len(groups) == 0 {
			return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, account)
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			if group <= 0 {
				return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, account, group); err != nil {
				return nil, err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE sharing_offers SET review_status=$1,enabled=$2,version=version+1,updated_at=NOW() WHERE id=$3`, status, status == "approved", id)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sharing_offer_review_audits(offer_id,admin_user_id,previous_status,new_status,reason) VALUES($1,$2,$3,$4,$5)`, id, admin, previous, status, reason)
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &account, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return r.Get(ctx, owner, account)
}

func (r *SupplierAccountRepository) ReplaceCredentials(ctx context.Context, user, id int64, key, digest, apiKey string, version int64) (*service.SupplierOperation, error) {
	if r == nil || r.db == nil || r.envelope == nil {
		return nil, service.ErrCredentialEnvelope
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = activeSupplier(ctx, tx, user); err != nil {
		return nil, err
	}
	var op service.SupplierOperation
	var previousDigest string
	err = tx.QueryRowContext(ctx, `INSERT INTO supplier_operations(user_id,operation_key,kind,request_digest,status) VALUES($1,$2,'credentials',$3,'processing') ON CONFLICT (user_id,operation_key) DO NOTHING RETURNING operation_key,kind,status,account_id,error_code`, user, key, digest).Scan(&op.Key, &op.Kind, &op.Status, &op.AccountID, &op.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT operation_key,kind,status,account_id,error_code,request_digest FROM supplier_operations WHERE user_id=$1 AND operation_key=$2`, user, key).Scan(&op.Key, &op.Kind, &op.Status, &op.AccountID, &op.ErrorCode, &previousDigest)
		if err != nil {
			return nil, err
		}
		if op.Kind != "credentials" || previousDigest != digest {
			return nil, apperrors.Conflict("idempotency_conflict", "Idempotency key reused")
		}
		return &op, nil
	}
	if err != nil {
		return nil, err
	}
	a, err := scanAccount(tx.QueryRowContext(ctx, supplierAccountSelect+` FOR UPDATE OF a,o`, id, user))
	if err != nil {
		return nil, err
	}
	if a.Offer.Version != version {
		return nil, versionConflict()
	}
	if a.Type != "apikey" {
		return nil, apperrors.BadRequest("invalid_credentials", "Account type requires authorization")
	}
	raw, err := json.Marshal(map[string]string{"api_key": apiKey})
	if err != nil {
		return nil, err
	}
	sealed, err := r.envelope.Seal(a.Platform, user, id, raw)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"_supplier_envelope": sealed})
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_strip_nulls(jsonb_build_object('base_url',credentials->'base_url','model_mapping',credentials->'model_mapping','openai_capabilities',credentials->'openai_capabilities')) || $1::jsonb,updated_at=NOW() WHERE id=$2 AND owner_user_id=$3`, string(payload), id, user)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM marketplace_credential_fingerprints WHERE account_id=$1`, id)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256([]byte(apiKey))
	result, err := tx.ExecContext(ctx, `INSERT INTO marketplace_credential_fingerprints(provider,fingerprint,account_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, a.Platform, hex.EncodeToString(fingerprint[:]), id)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, apperrors.Conflict("duplicate_credential", "Credential already registered")
	}
	_, err = tx.ExecContext(ctx, `UPDATE sharing_offers SET review_status='pending_review',enabled=false,version=version+1,revoked_at=NULL,updated_at=NOW() WHERE account_id=$1 AND owner_user_id=$2`, id, user)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE supplier_operations SET status='completed',account_id=$3,updated_at=NOW() WHERE user_id=$1 AND operation_key=$2`, user, key, id)
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	op.Status = "completed"
	op.AccountID = &id
	return &op, nil
}

const usageQuery = `SELECT COUNT(*),COALESCE(SUM(COALESCE((a.usage->>'input_tokens')::bigint,0)),0),COALESCE(SUM(COALESCE((a.usage->>'output_tokens')::bigint,0)),0),COALESCE(SUM(c.consumer_charge-c.refunded_charge),0)::text,COALESCE(SUM(c.supplier_net-c.refunded_net),0)::text FROM marketplace_captures c JOIN marketplace_attempts a ON a.id=c.attempt_id AND a.receipt_id=c.receipt_id`

func (r *SupplierAccountRepository) Overview(ctx context.Context, user int64) (*service.SupplierOverview, error) {
	result := &service.SupplierOverview{DailyUsage: []service.SupplierDailyUsage{}, UpdatedAt: time.Now().UTC()}
	err := r.db.QueryRowContext(ctx, usageQuery+` WHERE c.owner_user_id=$1`, user).Scan(&result.Stats.Requests, &result.Stats.InputTokens, &result.Stats.OutputTokens, &result.Stats.ConsumerCharge, &result.Stats.SupplierNet)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT to_char(c.created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),COUNT(*),COALESCE(SUM(COALESCE((a.usage->>'input_tokens')::bigint,0)),0),COALESCE(SUM(COALESCE((a.usage->>'output_tokens')::bigint,0)),0),COALESCE(SUM(c.consumer_charge-c.refunded_charge),0)::text,COALESCE(SUM(c.supplier_net-c.refunded_net),0)::text FROM marketplace_captures c JOIN marketplace_attempts a ON a.id=c.attempt_id AND a.receipt_id=c.receipt_id WHERE c.owner_user_id=$1 AND c.created_at>=now()-interval '30 days' GROUP BY 1 ORDER BY 1`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d service.SupplierDailyUsage
		if err = rows.Scan(&d.Date, &d.Requests, &d.InputTokens, &d.OutputTokens, &d.ConsumerCharge, &d.SupplierNet); err != nil {
			return nil, err
		}
		result.DailyUsage = append(result.DailyUsage, d)
	}
	return result, rows.Err()
}

func (r *SupplierAccountRepository) ReserveProbe(ctx context.Context, user, id int64) (string, error) {
	if r == nil || r.db == nil || r.envelope == nil {
		return "", service.ErrCredentialEnvelope
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = activeSupplier(ctx, tx, user); err != nil {
		return "", err
	}
	var platform, typ string
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT platform,type,credentials FROM accounts WHERE id=$1 AND owner_user_id=$2 AND parent_account_id IS NULL AND deleted_at IS NULL FOR SHARE`, id, user).Scan(&platform, &typ, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", accountNotFound()
	}
	if err != nil {
		return "", err
	}
	if platform != "openai" || typ != "apikey" {
		return "", apperrors.Conflict("probe_unavailable", "Probe unavailable")
	}
	var payload struct {
		Envelope service.EncryptedCredential `json:"_supplier_envelope"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return "", service.ErrCredentialEnvelope
	}
	plain, err := r.envelope.Open(platform, user, id, payload.Envelope)
	if err != nil {
		return "", service.ErrCredentialEnvelope
	}
	var credentials struct {
		APIKey string `json:"api_key"`
	}
	if json.Unmarshal(plain, &credentials) != nil || credentials.APIKey == "" {
		return "", service.ErrCredentialEnvelope
	}
	var reserved int64
	err = tx.QueryRowContext(ctx, `INSERT INTO supplier_probe_limits(account_id,last_probe_at) VALUES($1,NOW()) ON CONFLICT (account_id) DO UPDATE SET last_probe_at=NOW() WHERE supplier_probe_limits.last_probe_at<NOW()-interval '1 minute' RETURNING account_id`, id).Scan(&reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return "", apperrors.Conflict("probe_rate_limited", "Please wait before probing again")
	}
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return credentials.APIKey, nil
}
