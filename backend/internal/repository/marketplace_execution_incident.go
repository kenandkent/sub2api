package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const executionIncidentColumns = `operation_id,receipt_id::text,attempt_id,release_operation_id,actor_user_id,quote_digest,evidence,created_at`

func scanExecutionIncident(row marketplaceRowScanner) (*service.MarketplaceExecutionIncident, error) {
	var v service.MarketplaceExecutionIncident
	if err := row.Scan(&v.OperationID, &v.ReceiptID, &v.AttemptID, &v.ReleaseOperationID, &v.ActorUserID, &v.QuoteDigest, &v.Evidence, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.Status = "recorded_manual_review"
	return &v, nil
}
func validIncidentText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func lockIncidentAdmin(ctx context.Context, tx *sql.Tx, actor int64) error {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT role='admin' AND status='active' AND deleted_at IS NULL FROM users WHERE id=$1 FOR SHARE`, actor).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !active) {
		return service.ErrSupplierFinanceInvalid
	}
	return err
}

func (r *MarketplaceGatewayRepository) RegisterExecutionIncident(ctx context.Context, actor int64, receipt string, attempt int64, release, evidence, operation string) (*service.MarketplaceExecutionIncident, error) {
	release = strings.TrimSpace(release)
	evidence = strings.TrimSpace(evidence)
	operation = strings.TrimSpace(operation)
	id, err := uuid.Parse(receipt)
	if err != nil || actor <= 0 || attempt <= 0 || !validIncidentText(release, 255) || !validIncidentText(evidence, 4000) || !validIncidentText(operation, 255) {
		return nil, service.ErrSupplierFinanceInvalid
	}
	receipt = id.String()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockIncidentAdmin(ctx, tx, actor); err != nil {
		return nil, err
	}
	// Serialize repeated operations before validating current receipt state.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "marketplace:execution-incident:"+operation); err != nil {
		return nil, err
	}
	prior, err := scanExecutionIncident(tx.QueryRowContext(ctx, `SELECT `+executionIncidentColumns+` FROM marketplace_execution_incidents WHERE operation_id=$1`, operation))
	if err == nil {
		if prior.ReceiptID != receipt || prior.AttemptID != attempt || prior.ReleaseOperationID != release || prior.Evidence != evidence || prior.ActorUserID != actor {
			return nil, service.ErrSupplierFinanceConflict
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM marketplace_requests WHERE id=$1 FOR UPDATE`, receipt).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierFinanceInvalid
	} else if err != nil {
		return nil, err
	}
	if state != "voided" {
		return nil, service.ErrSupplierFinanceInvalid
	}
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT quote_digest FROM marketplace_attempts WHERE id=$1 AND receipt_id=$2 AND status='rejected_before_execution' FOR SHARE`, attempt, receipt).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierFinanceInvalid
	}
	if err != nil {
		return nil, err
	}
	var audit string
	err = tx.QueryRowContext(ctx, `SELECT operation_id FROM marketplace_recovery_audits WHERE operation_id=$1 AND receipt_id=$2 AND action='release_confirmed_unexecuted' FOR SHARE`, release, receipt).Scan(&audit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierFinanceInvalid
	}
	if err != nil {
		return nil, err
	}
	v, err := scanExecutionIncident(tx.QueryRowContext(ctx, `INSERT INTO marketplace_execution_incidents(operation_id,receipt_id,attempt_id,release_operation_id,actor_user_id,quote_digest,evidence) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+executionIncidentColumns, operation, receipt, attempt, release, actor, digest, evidence))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return v, nil
}
func (r *MarketplaceGatewayRepository) ListExecutionIncidents(ctx context.Context, actor int64, receipt string) ([]service.MarketplaceExecutionIncident, error) {
	id, err := uuid.Parse(receipt)
	if err != nil || actor <= 0 {
		return nil, service.ErrSupplierFinanceInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockIncidentAdmin(ctx, tx, actor); err != nil {
		return nil, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_requests WHERE id=$1)`, id.String()).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, service.ErrSupplierFinanceNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+executionIncidentColumns+` FROM marketplace_execution_incidents WHERE receipt_id=$1 ORDER BY created_at,operation_id`, id.String())
	if err != nil {
		return nil, err
	}
	var items []service.MarketplaceExecutionIncident
	for rows.Next() {
		v, e := scanExecutionIncident(rows)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		items = append(items, *v)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if items == nil {
		items = []service.MarketplaceExecutionIncident{}
	}
	return items, nil
}
