package repository

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

type SupplierPayoutRepository struct {
	db     *sql.DB
	ledger *MarketplaceLedgerRepository
}

func NewSupplierPayoutRepository(db *sql.DB) *SupplierPayoutRepository {
	return &SupplierPayoutRepository{db: db, ledger: NewMarketplaceLedgerRepository()}
}
func payoutFingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func payoutOp(ctx context.Context, tx *sql.Tx, id, fingerprint string, result any) (bool, error) {
	if strings.TrimSpace(id) == "" || len(id) > 255 {
		return false, service.ErrPayoutInvalid
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id); err != nil {
		return false, err
	}
	var f string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,result FROM supplier_payout_operations WHERE operation_id=$1`, id).Scan(&f, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if f != fingerprint {
		return false, service.ErrPayoutInvalid
	}
	return true, json.Unmarshal(raw, result)
}
func savePayoutOp(ctx context.Context, tx *sql.Tx, id, fingerprint, kind string, resultID, actor int64, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO supplier_payout_operations(operation_id,fingerprint,kind,result_id,actor_id,result) VALUES ($1,$2,$3,$4,$5,$6)`, id, fingerprint, kind, resultID, actor, b)
	return err
}
func (r *SupplierPayoutRepository) CreateDestination(ctx context.Context, owner int64, mask string, secret []byte, op string, envelope *service.CredentialEnvelope) (*service.PayoutDestination, error) {
	if owner <= 0 || strings.TrimSpace(mask) == "" || len(secret) == 0 || strings.Contains(mask, string(secret)) || envelope == nil {
		return nil, service.ErrPayoutInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	fp := payoutFingerprint([]any{"destination", owner, mask})
	var result service.PayoutDestination
	found, err := payoutOp(ctx, tx, op, fp, &result)
	if err != nil {
		return nil, err
	}
	if found {
		var raw []byte
		if err = tx.QueryRowContext(ctx, `SELECT credential FROM supplier_payout_destinations WHERE id=$1 AND supplier_id=$2`, result.ID, owner).Scan(&raw); err != nil {
			return nil, err
		}
		var stored service.EncryptedCredential
		if err = json.Unmarshal(raw, &stored); err != nil {
			return nil, err
		}
		plain, err := envelope.Open("payout", owner, result.ID, stored)
		if err != nil {
			return nil, err
		}
		if subtle.ConstantTimeCompare(plain, secret) != 1 {
			return nil, service.ErrPayoutInvalid
		}
		return &result, tx.Commit()
	}
	var ownerLock int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&ownerLock); err != nil {
		return nil, err
	}
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('supplier_payout_destinations','id'))`).Scan(&id); err != nil {
		return nil, err
	}
	enc, err := envelope.Seal("payout", owner, id, secret)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(enc)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO supplier_payout_destinations(id,supplier_id,masked_label,credential,version) VALUES ($1,$2,$3,$4,(SELECT coalesce(max(version),0)+1 FROM supplier_payout_destinations WHERE supplier_id=$2)) RETURNING id,supplier_id,masked_label,version,verified,created_at`, id, owner, mask, raw).Scan(&result.ID, &result.SupplierID, &result.MaskedLabel, &result.Version, &result.Verified, &result.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err = savePayoutOp(ctx, tx, op, fp, "destination", id, owner, result); err != nil {
		return nil, err
	}
	return &result, tx.Commit()
}
func isPayoutAdmin(ctx context.Context, tx *sql.Tx, id int64) bool {
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin' AND status='active' AND deleted_at IS NULL)`, id).Scan(&ok)
	return err == nil && ok
}
func lockPayoutAdmin(ctx context.Context, tx *sql.Tx, id int64) bool {
	var role, status string
	err := tx.QueryRowContext(ctx, `SELECT role,status FROM users WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&role, &status)
	return err == nil && role == "admin" && status == "active"
}
func (r *SupplierPayoutRepository) VerifyDestination(ctx context.Context, id, admin int64, op, evidence string) (*service.PayoutDestination, error) {
	if strings.TrimSpace(evidence) == "" || len(evidence) > 4096 {
		return nil, service.ErrPayoutInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	fp := payoutFingerprint([]any{"verify", id, admin, evidence})
	var d service.PayoutDestination
	found, err := payoutOp(ctx, tx, op, fp, &d)
	if err != nil {
		return nil, err
	}
	if !lockPayoutAdmin(ctx, tx, admin) {
		return nil, service.ErrPayoutInvalid
	}
	if found {
		return &d, tx.Commit()
	}
	err = tx.QueryRowContext(ctx, `UPDATE supplier_payout_destinations SET verified=true,verified_by=$2,verified_at=now(),verification_evidence=$3 WHERE id=$1 AND supplier_id<>$2 AND NOT verified RETURNING id,supplier_id,masked_label,version,verified,created_at`, id, admin, evidence).Scan(&d.ID, &d.SupplierID, &d.MaskedLabel, &d.Version, &d.Verified, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err = savePayoutOp(ctx, tx, op, fp, "verify", id, admin, d); err != nil {
		return nil, err
	}
	return &d, tx.Commit()
}
func (r *SupplierPayoutRepository) ListDestinations(ctx context.Context, owner int64) ([]service.PayoutDestination, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,supplier_id,masked_label,version,verified,created_at FROM supplier_payout_destinations WHERE supplier_id=$1 ORDER BY id DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.PayoutDestination{}
	for rows.Next() {
		var d service.PayoutDestination
		if err = rows.Scan(&d.ID, &d.SupplierID, &d.MaskedLabel, &d.Version, &d.Verified, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type payoutWallet struct {
	available, reserved, debt decimal.Decimal
	status                    string
	frozen                    bool
}

func lockPayoutWallet(ctx context.Context, tx *sql.Tx, owner int64) (payoutWallet, error) {
	var w payoutWallet
	err := tx.QueryRowContext(ctx, `SELECT available,payout_reserved,debt FROM supplier_wallets WHERE user_id=$1 AND currency='USD' FOR UPDATE`, owner).Scan(&w.available, &w.reserved, &w.debt)
	if err != nil {
		return w, err
	}
	err = tx.QueryRowContext(ctx, `SELECT status,payout_frozen FROM supplier_profiles WHERE user_id=$1 FOR UPDATE`, owner).Scan(&w.status, &w.frozen)
	return w, err
}
func canPayout(w payoutWallet) bool { return w.status == "active" && !w.frozen && w.debt.IsZero() }
func scanPayout(row interface{ Scan(...any) error }) (*service.SupplierPayout, error) {
	var p service.SupplierPayout
	err := row.Scan(&p.ID, &p.SupplierID, &p.DestinationID, &p.MaskedLabel, &p.Amount, &p.Currency, &p.Status, &p.ExternalReference, &p.PaidAt, &p.Evidence, &p.CreatedAt)
	return &p, err
}

const payoutColumns = `id,supplier_id,destination_id,masked_label,amount,currency,status,external_reference,paid_at,evidence,created_at`

func (r *SupplierPayoutRepository) Request(ctx context.Context, owner, destination int64, amount decimal.Decimal, op string) (*service.SupplierPayout, error) {
	if owner <= 0 || destination <= 0 || service.ValidatePayoutAmount(amount, decimal.Zero) != nil {
		return nil, service.ErrPayoutInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	fp := payoutFingerprint([]any{"request", owner, destination, amount.String()})
	var result service.SupplierPayout
	found, err := payoutOp(ctx, tx, op, fp, &result)
	if err != nil {
		return nil, err
	}
	if found {
		return &result, tx.Commit()
	}
	w, err := lockPayoutWallet(ctx, tx, owner)
	if err != nil {
		return nil, err
	}
	if !canPayout(w) || w.available.LessThan(amount) {
		return nil, service.ErrPayoutInvalid
	}
	p, err := scanPayout(tx.QueryRowContext(ctx, `INSERT INTO supplier_payouts(supplier_id,destination_id,masked_label,amount,status) SELECT $1,id,masked_label,$3,'requested' FROM supplier_payout_destinations WHERE id=$2 AND supplier_id=$1 AND verified RETURNING `+payoutColumns, owner, destination, amount))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE supplier_wallets SET available=available-$2,payout_reserved=payout_reserved+$2,version=version+1 WHERE user_id=$1 AND currency='USD'`, owner, amount)
	if err != nil {
		return nil, err
	}
	err = r.ledger.Post(ctx, tx, "payout:request:"+op, fmt.Sprintf("user:%d", owner), "USD", []LedgerPosting{{Account: "supplier_available", SubjectID: &owner, Side: "debit", Amount: amount}, {Account: "supplier_payout_reserved", SubjectID: &owner, Side: "credit", Amount: amount}})
	if err != nil {
		return nil, err
	}
	if err = savePayoutOp(ctx, tx, op, fp, "request", p.ID, owner, p); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

func (r *SupplierPayoutRepository) release(ctx context.Context, tx *sql.Tx, owner int64, amount decimal.Decimal, op, actor string) error {
	var debt decimal.Decimal
	if err := tx.QueryRowContext(ctx, `SELECT debt FROM supplier_wallets WHERE user_id=$1 AND currency='USD'`, owner).Scan(&debt); err != nil {
		return err
	}
	paid, available := service.PayoutRelease(amount, debt)
	res, err := tx.ExecContext(ctx, `UPDATE supplier_wallets SET payout_reserved=payout_reserved-$2,debt=debt-$3,available=available+$4,version=version+1 WHERE user_id=$1 AND currency='USD' AND payout_reserved >= $2`, owner, amount, paid, available)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrPayoutInvalid
	}
	entries := []LedgerPosting{{Account: "supplier_payout_reserved", SubjectID: &owner, Side: "debit", Amount: amount}}
	if paid.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "supplier_debt", SubjectID: &owner, Side: "credit", Amount: paid})
	}
	if available.IsPositive() {
		entries = append(entries, LedgerPosting{Account: "supplier_available", SubjectID: &owner, Side: "credit", Amount: available})
	}
	return r.ledger.Post(ctx, tx, op, actor, "USD", entries)
}
func (r *SupplierPayoutRepository) Action(ctx context.Context, a service.PayoutAction) (*service.SupplierPayout, error) {
	if a.ActorID <= 0 || a.PayoutID <= 0 || strings.TrimSpace(a.OperationID) == "" {
		return nil, service.ErrPayoutInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if a.Action != "cancel" && !isPayoutAdmin(ctx, tx, a.ActorID) {
		return nil, service.ErrPayoutInvalid
	}
	fp := payoutFingerprint([]any{"action", a})
	var original service.SupplierPayout
	found, err := payoutOp(ctx, tx, a.OperationID, fp, &original)
	if err != nil {
		return nil, err
	}
	if found {
		return &original, tx.Commit()
	}
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT supplier_id FROM supplier_payouts WHERE id=$1`, a.PayoutID).Scan(&owner); err != nil {
		return nil, err
	}
	w, err := lockPayoutWallet(ctx, tx, owner)
	if err != nil {
		return nil, err
	}
	p, err := scanPayout(tx.QueryRowContext(ctx, `SELECT `+payoutColumns+` FROM supplier_payouts WHERE id=$1 FOR UPDATE`, a.PayoutID))
	if err != nil {
		return nil, err
	}
	admin := isPayoutAdmin(ctx, tx, a.ActorID)
	next := ""
	release := false
	switch a.Action {
	case "approve":
		if !admin || a.ActorID == owner || p.Status != "requested" || !canPayout(w) || w.reserved.LessThan(p.Amount) {
			return nil, service.ErrPayoutInvalid
		}
		next = "approved"
	case "start":
		if !admin || p.Status != "approved" || !canPayout(w) || w.reserved.LessThan(p.Amount) {
			return nil, service.ErrPayoutInvalid
		}
		next = "processing"
	case "confirm":
		if !admin || a.ActorID == owner || p.Status != "processing" || strings.TrimSpace(a.ExternalReference) == "" || strings.TrimSpace(a.Evidence) == "" || a.Currency != "USD" || !a.Amount.Equal(p.Amount) || a.PaidAt == nil || a.PaidAt.IsZero() || w.reserved.LessThan(p.Amount) {
			return nil, service.ErrPayoutInvalid
		}
		next = "paid"
	case "fail":
		if !admin || p.Status != "processing" || strings.TrimSpace(a.Evidence) == "" {
			return nil, service.ErrPayoutInvalid
		}
		next = "failed_confirmed"
		release = true
	case "reject":
		if !admin || (p.Status != "requested" && p.Status != "approved") || strings.TrimSpace(a.Evidence) == "" {
			return nil, service.ErrPayoutInvalid
		}
		next = "rejected"
		release = true
	case "cancel":
		if p.Status != "requested" && p.Status != "approved" {
			return nil, service.ErrPayoutInvalid
		}
		if a.ActorID == owner {
			if p.Status != "requested" {
				return nil, service.ErrPayoutInvalid
			}
		} else if !admin || strings.TrimSpace(a.Evidence) == "" {
			return nil, service.ErrPayoutInvalid
		}
		next = "cancelled"
		release = true
	default:
		return nil, service.ErrPayoutInvalid
	}
	actor := fmt.Sprintf("user:%d", a.ActorID)
	if release {
		if err = r.release(ctx, tx, owner, p.Amount, "payout:action:"+a.OperationID, actor); err != nil {
			return nil, err
		}
	}
	if next == "paid" {
		_, err = tx.ExecContext(ctx, `UPDATE supplier_wallets SET payout_reserved=payout_reserved-$2,version=version+1 WHERE user_id=$1 AND currency='USD'`, owner, p.Amount)
		if err != nil {
			return nil, err
		}
		err = r.ledger.Post(ctx, tx, "payout:action:"+a.OperationID, actor, "USD", []LedgerPosting{{Account: "supplier_payout_reserved", SubjectID: &owner, Side: "debit", Amount: p.Amount}, {Account: "supplier_paid", SubjectID: &owner, Side: "credit", Amount: p.Amount}})
		if err != nil {
			return nil, err
		}
	}
	var ext any
	var paidAt any
	if next == "paid" {
		ext = strings.TrimSpace(a.ExternalReference)
		paidAt = a.PaidAt
	}
	var evidence any
	if strings.TrimSpace(a.Evidence) != "" {
		evidence = a.Evidence
	}
	p, err = scanPayout(tx.QueryRowContext(ctx, `UPDATE supplier_payouts SET status=$2,external_reference=$3,evidence=$4,paid_at=$5,updated_at=now() WHERE id=$1 RETURNING `+payoutColumns, p.ID, next, ext, evidence, paidAt))
	if err != nil {
		return nil, err
	}
	if err = savePayoutOp(ctx, tx, a.OperationID, fp, a.Action, p.ID, a.ActorID, p); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

// CancelPreProcessingTx must be called with the supplier wallet row already locked.
// It leaves processing payouts untouched and records each release in the caller's transaction.
func (r *SupplierPayoutRepository) CancelPreProcessingTx(ctx context.Context, tx *sql.Tx, owner int64, operationID, actor string) error {
	if tx == nil || owner <= 0 || strings.TrimSpace(operationID) == "" || strings.TrimSpace(actor) == "" {
		return service.ErrPayoutInvalid
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,amount FROM supplier_payouts WHERE supplier_id=$1 AND status IN ('requested','approved') ORDER BY id FOR UPDATE`, owner)
	if err != nil {
		return err
	}
	type item struct {
		id     int64
		amount decimal.Decimal
	}
	var items []item
	for rows.Next() {
		var p item
		if err = rows.Scan(&p.id, &p.amount); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range items {
		if err = r.release(ctx, tx, owner, p.amount, fmt.Sprintf("payout:risk:%s:%d", operationID, p.id), actor); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE supplier_payouts SET status='risk_cancelled',evidence='refund risk cancellation',updated_at=now() WHERE id=$1`, p.id); err != nil {
			return err
		}
	}
	return nil
}
func (r *SupplierPayoutRepository) List(ctx context.Context, owner int64) ([]service.SupplierPayout, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+payoutColumns+` FROM supplier_payouts WHERE supplier_id=$1 ORDER BY id DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.SupplierPayout{}
	for rows.Next() {
		p, err := scanPayout(rows)
		if err != nil {
			return nil, err
		}
		p.Evidence = nil
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (r *SupplierPayoutRepository) Get(ctx context.Context, id, actor int64, admin bool) (*service.SupplierPayout, error) {
	if admin {
		return scanPayout(r.db.QueryRowContext(ctx, `SELECT `+payoutColumns+` FROM supplier_payouts WHERE id=$1 AND EXISTS (SELECT 1 FROM users WHERE id=$2 AND role='admin' AND status='active' AND deleted_at IS NULL)`, id, actor))
	}
	p, err := scanPayout(r.db.QueryRowContext(ctx, `SELECT `+payoutColumns+` FROM supplier_payouts WHERE id=$1 AND supplier_id=$2`, id, actor))
	if err != nil {
		return nil, err
	}
	p.Evidence = nil
	return p, nil
}

func (r *SupplierPayoutRepository) ListAdmin(ctx context.Context, actor int64) ([]service.SupplierPayout, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+payoutColumns+` FROM supplier_payouts WHERE EXISTS (SELECT 1 FROM users WHERE id=$1 AND role='admin' AND status='active' AND deleted_at IS NULL) ORDER BY id DESC`, actor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.SupplierPayout{}
	for rows.Next() {
		p, err := scanPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// FindRequest recovers a successful request when policy no longer permits new payouts.
func (r *SupplierPayoutRepository) FindRequest(ctx context.Context, owner, destination int64, amount decimal.Decimal, op string) (*service.SupplierPayout, error) {
	var fingerprint string
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT fingerprint,result FROM supplier_payout_operations WHERE operation_id=$1 AND kind='request' AND actor_id=$2`, op, owner).Scan(&fingerprint, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrPayoutInvalid
	}
	if err != nil {
		return nil, err
	}
	if fingerprint != payoutFingerprint([]any{"request", owner, destination, amount.String()}) {
		return nil, service.ErrPayoutInvalid
	}
	var result service.SupplierPayout
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListAdminDestinations returns review metadata only after checking current admin status.
func (r *SupplierPayoutRepository) ListAdminDestinations(ctx context.Context, actor int64) ([]service.AdminPayoutDestination, error) {
	var ok bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin' AND status='active' AND deleted_at IS NULL)`, actor).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, service.ErrPayoutInvalid
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,supplier_id,masked_label,version,verified,created_at,verified_by,verified_at,verification_evidence FROM supplier_payout_destinations ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.AdminPayoutDestination{}
	for rows.Next() {
		var d service.AdminPayoutDestination
		if err = rows.Scan(&d.ID, &d.SupplierID, &d.MaskedLabel, &d.Version, &d.Verified, &d.CreatedAt, &d.VerifiedBy, &d.VerifiedAt, &d.VerificationEvidence); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevealDestination records a durable reason before returning plaintext to an authorized operator.
func (r *SupplierPayoutRepository) RevealDestination(ctx context.Context, id, actor int64, reason, op string, env *service.CredentialEnvelope) ([]byte, error) {
	if id <= 0 || actor <= 0 || strings.TrimSpace(reason) == "" || len(reason) > 1024 || env == nil {
		return nil, service.ErrPayoutInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	fp := payoutFingerprint([]any{"reveal", id, actor, reason})
	var result struct {
		ID int64 `json:"id"`
	}
	found, err := payoutOp(ctx, tx, op, fp, &result)
	if err != nil {
		return nil, err
	}
	if !lockPayoutAdmin(ctx, tx, actor) {
		return nil, service.ErrPayoutInvalid
	}
	var owner int64
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT supplier_id,credential FROM supplier_payout_destinations WHERE id=$1`, id).Scan(&owner, &raw); err != nil {
		return nil, err
	}
	var enc service.EncryptedCredential
	if err = json.Unmarshal(raw, &enc); err != nil {
		return nil, err
	}
	plain, err := env.Open("payout", owner, id, enc)
	if err != nil {
		return nil, err
	}
	if len(plain) > 0 && (strings.Contains(reason, string(plain)) || strings.Contains(op, string(plain))) {
		return nil, service.ErrPayoutInvalid
	}
	if !found {
		result.ID = id
		if err = savePayoutOp(ctx, tx, op, fp, "reveal", id, actor, result); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE supplier_payout_operations SET reason=$2 WHERE operation_id=$1`, op, reason); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return plain, nil
}

// LookupOperation never exposes an operation owned by another user or of another kind.
func (r *SupplierPayoutRepository) LookupOperation(ctx context.Context, owner int64, kind, key string) (*service.PayoutOperationStatus, error) {
	result := &service.PayoutOperationStatus{OperationStatus: "not_found"}
	var id int64
	var status sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT op.result_id,p.status FROM supplier_payout_operations op LEFT JOIN supplier_payouts p ON op.kind='request' AND p.id=op.result_id AND p.supplier_id=op.actor_id WHERE op.actor_id=$1 AND op.kind=$2 AND op.operation_id=$3`, owner, kind, key).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if kind == "request" {
		if !status.Valid {
			return nil, service.ErrPayoutInvalid
		}
		result.PayoutID, result.PayoutStatus = &id, status.String
	} else {
		result.DestinationID = &id
	}
	result.OperationStatus = "completed"
	return result, nil
}

// SetPayoutFrozen serializes with refunds and payout transitions through the
// wallet/profile locks. The operation row is the immutable decision audit.
func (r *SupplierPayoutRepository) SetPayoutFrozen(ctx context.Context, cmd service.PayoutFreezeCommand) (*service.PayoutFreezeResult, error) {
	cmd.Reason = strings.TrimSpace(cmd.Reason)
	cmd.OperationID = strings.TrimSpace(cmd.OperationID)
	if cmd.SupplierID <= 0 || cmd.ActorID <= 0 || cmd.ExpectedVersion <= 0 || cmd.Reason == "" || len(cmd.Reason) > 4096 || cmd.OperationID == "" || len(cmd.OperationID) > 255 {
		return nil, service.ErrPayoutInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	fp := payoutFingerprint([]any{"freeze", cmd.SupplierID, cmd.ActorID, cmd.ExpectedVersion, cmd.Frozen, cmd.Reason})
	var result service.PayoutFreezeResult
	found, err := payoutOp(ctx, tx, cmd.OperationID, fp, &result)
	if err != nil {
		return nil, err
	}
	if !lockPayoutAdmin(ctx, tx, cmd.ActorID) {
		return nil, service.ErrPayoutInvalid
	}
	if found {
		return &result, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO supplier_wallets(user_id) VALUES($1) ON CONFLICT(user_id,currency) DO NOTHING`, cmd.SupplierID); err != nil {
		return nil, err
	}
	w, err := lockPayoutWallet(ctx, tx, cmd.SupplierID)
	if err != nil {
		return nil, err
	}
	if !cmd.Frozen && w.debt.IsPositive() {
		return nil, service.ErrPayoutInvalid
	}
	result.SupplierID = cmd.SupplierID
	result.Frozen = cmd.Frozen
	err = tx.QueryRowContext(ctx, `UPDATE supplier_profiles SET payout_frozen=$2, risk_reason=$3, version=version+1, updated_at=now() WHERE user_id=$1 AND version=$4 RETURNING version`, cmd.SupplierID, cmd.Frozen, cmd.Reason, cmd.ExpectedVersion).Scan(&result.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrPayoutInvalid
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO supplier_payout_operations(operation_id,fingerprint,kind,result_id,actor_id,result,reason) VALUES($1,$2,'freeze',$3,$4,$5,$6)`, cmd.OperationID, fp, cmd.SupplierID, cmd.ActorID, raw, cmd.Reason)
	if err != nil {
		return nil, err
	}
	return &result, tx.Commit()
}
