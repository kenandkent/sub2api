package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *usageBillingRepository) BeginOpenCodeFreeAttempt(ctx context.Context, m *service.OpenCodeFreeUsageMeta, apiKeyID, accountID int64, profileHash, payloadHash string, timeout time.Duration) error {
	if r == nil || r.db == nil || m == nil {
		return errors.New("OpenCode free repository unavailable")
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO opencode_free_attempts (request_id,lease_token,api_key_id,user_id,account_id,profile_hash,payload_hash,state,lease_until)
 SELECT $1,$2,id,user_id,$4,$5,$6,'pending',now()+($7 * interval '1 second') FROM api_keys WHERE id=$3 AND deleted_at IS NULL`, m.RequestID, m.LeaseToken, apiKeyID, accountID, profileHash, payloadHash, timeout.Seconds()+60)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}
func (r *usageBillingRepository) MarkOpenCodeFreeUnknown(ctx context.Context, m *service.OpenCodeFreeUsageMeta, _ string) error {
	if m == nil {
		return errors.New("invalid attempt")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE opencode_free_attempts SET state='unknown',updated_at=now() WHERE request_id=$1 AND lease_token=$2 AND state='pending'`, m.RequestID, m.LeaseToken)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("OpenCode free attempt is not pending")
	}
	return nil
}
func (r *usageBillingRepository) ResolveOpenCodeFreeSession(ctx context.Context, hash, candidate string, ttl time.Duration) (string, error) {
	var session string
	err := r.db.QueryRowContext(ctx, `INSERT INTO opencode_free_sessions(key_hash,session_id,expires_at) VALUES($1,$2,now()+($3 * interval '1 second'))
 ON CONFLICT(key_hash) DO UPDATE SET session_id=CASE WHEN opencode_free_sessions.expires_at<=now() THEN EXCLUDED.session_id ELSE opencode_free_sessions.session_id END,
 expires_at=EXCLUDED.expires_at RETURNING session_id`, hash, candidate, ttl.Seconds()).Scan(&session)
	return session, err
}

type freeStoredCommand struct {
	service.UsageBillingCommand
	Context *service.OpenCodeFreeBillingContext `json:"_opencode_free_context"`
}

func (r *usageBillingRepository) StageOpenCodeFreeUsage(ctx context.Context, m *service.OpenCodeFreeUsageMeta, cmd *service.UsageBillingCommand, log *service.UsageLog, billingContext *service.OpenCodeFreeBillingContext) error {
	if m == nil || cmd == nil || log == nil || billingContext == nil || !m.Complete || !json.Valid(m.RawUsage) || m.BillingSource == "" {
		return errors.New("invalid OpenCode free snapshot")
	}
	if m.RequestID != cmd.RequestID || m.RequestID != log.RequestID || cmd.APIKeyID != log.APIKeyID || cmd.UserID != log.UserID || cmd.AccountID != log.AccountID {
		return errors.New("OpenCode free snapshot identity mismatch")
	}
	command, err := json.Marshal(freeStoredCommand{UsageBillingCommand: *cmd, Context: billingContext})
	if err != nil {
		return err
	}
	usageLog, err := json.Marshal(log)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE opencode_free_attempts SET state='ready',billing_source=$3,raw_usage=$4::jsonb,billing_command=$5::jsonb,usage_log=$6::jsonb,updated_at=now()
 WHERE request_id=$1 AND lease_token=$2 AND state='pending' AND lease_until>now() AND api_key_id=$7 AND user_id=$8 AND account_id=$9 AND payload_hash=$10`, m.RequestID, m.LeaseToken, m.BillingSource, string(m.RawUsage), string(command), string(usageLog), cmd.APIKeyID, cmd.UserID, cmd.AccountID, cmd.RequestPayloadHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	// A retry may reuse the first frozen price only when identity and usage match.
	var match bool
	err = r.db.QueryRowContext(ctx, `SELECT state IN ('ready','settled') AND lease_token=$2 AND api_key_id=$3 AND user_id=$4 AND account_id=$5 AND payload_hash=$6 AND billing_source=$7 AND raw_usage=$8::jsonb FROM opencode_free_attempts WHERE request_id=$1`, m.RequestID, m.LeaseToken, cmd.APIKeyID, cmd.UserID, cmd.AccountID, cmd.RequestPayloadHash, m.BillingSource, string(m.RawUsage)).Scan(&match)
	if err != nil {
		return err
	}
	if !match {
		return errors.New("OpenCode free snapshot conflict")
	}
	return nil
}
func (r *usageBillingRepository) SettleOpenCodeFreeUsage(ctx context.Context, requestID string) (_ *service.OpenCodeFreeSettlement, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var state string
	var command, logData []byte
	err = tx.QueryRowContext(ctx, `SELECT state,billing_command,usage_log FROM opencode_free_attempts WHERE request_id=$1 FOR UPDATE`, requestID).Scan(&state, &command, &logData)
	if err != nil {
		return nil, err
	}
	if state == "settled" {
		return &service.OpenCodeFreeSettlement{}, nil
	}
	if state != "ready" {
		return nil, fmt.Errorf("OpenCode free attempt is %s", state)
	}
	var stored freeStoredCommand
	var log service.UsageLog
	if err = json.Unmarshal(command, &stored); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(logData, &log); err != nil {
		return nil, err
	}
	if stored.Context == nil {
		return nil, errors.New("OpenCode free snapshot lacks billing context")
	}
	cmd := stored.UsageBillingCommand
	if cmd.RequestID != requestID || log.RequestID != requestID {
		return nil, errors.New("OpenCode free snapshot identity mismatch")
	}
	applied, err := r.claimUsageBillingKey(ctx, tx, &cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, errors.New("OpenCode free billing key already claimed")
	}
	result := &service.UsageBillingApplyResult{Applied: true}
	if err = r.applyUsageBillingEffects(ctx, tx, &cmd, result); err != nil {
		return nil, err
	}
	if err = execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(&log)); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE opencode_free_attempts SET state='settled',updated_at=now() WHERE request_id=$1`, requestID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &service.OpenCodeFreeSettlement{Result: *result, Command: cmd, Log: log, BillingContext: *stored.Context}, nil
}
func (r *usageBillingRepository) RecoverOpenCodeFreeUsage(ctx context.Context) ([]service.OpenCodeFreeRecoveredUsage, error) {
	if _, err := r.db.ExecContext(ctx, `UPDATE opencode_free_attempts SET state='unknown',updated_at=now() WHERE state='pending' AND lease_until<=now()`); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT request_id FROM opencode_free_attempts WHERE state='ready' AND updated_at < now() - interval '60 seconds' ORDER BY updated_at,request_id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var recovered []service.OpenCodeFreeRecoveredUsage
	var errs []error
	for _, id := range ids {
		result, settleErr := r.SettleOpenCodeFreeUsage(ctx, id)
		if settleErr != nil && !errors.Is(settleErr, sql.ErrNoRows) {
			errs = append(errs, settleErr)
			// Failed rows retain their frozen data but move behind other ready work.
			if _, updateErr := r.db.ExecContext(ctx, `UPDATE opencode_free_attempts SET updated_at=now() WHERE request_id=$1 AND state='ready'`, id); updateErr != nil {
				errs = append(errs, updateErr)
			}
			continue
		}
		if result != nil && result.Result.Applied {
			recovered = append(recovered, service.OpenCodeFreeRecoveredUsage{Command: result.Command, Log: result.Log})
		}
	}
	return recovered, errors.Join(errs...)
}
