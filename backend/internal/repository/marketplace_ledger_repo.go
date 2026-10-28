package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// LedgerPosting describes one positive debit or credit in a single currency.
type LedgerPosting struct {
	Account   string
	SubjectID *int64
	Side      string
	Amount    decimal.Decimal
}
type MarketplaceLedgerRepository struct{}

func NewMarketplaceLedgerRepository() *MarketplaceLedgerRepository {
	return &MarketplaceLedgerRepository{}
}

// Post uses the caller's transaction. The caller must roll it back after any error.
func (r *MarketplaceLedgerRepository) Post(ctx context.Context, tx *sql.Tx, operationID, actor, currency string, entries []LedgerPosting) error {
	return r.post(ctx, tx, operationID, actor, currency, nil, entries)
}

// PostWithOriginal appends a transaction referencing its immutable original.
func (r *MarketplaceLedgerRepository) PostWithOriginal(ctx context.Context, tx *sql.Tx, operationID, actor, currency string, original int64, entries []LedgerPosting) error {
	if original <= 0 {
		return errors.New("invalid original transaction")
	}
	return r.post(ctx, tx, operationID, actor, currency, &original, entries)
}

func (r *MarketplaceLedgerRepository) post(ctx context.Context, tx *sql.Tx, operationID, actor, currency string, original *int64, entries []LedgerPosting) error {
	if tx == nil || operationID == "" || actor == "" || currency != "USD" || len(entries) < 2 {
		return errors.New("invalid ledger transaction")
	}
	sum := decimal.Zero
	for _, e := range entries {
		if e.Account == "" || !e.Amount.IsPositive() || !e.Amount.Equal(e.Amount.Round(8)) {
			return errors.New("invalid ledger entry")
		}
		switch e.Side {
		case "debit":
			sum = sum.Add(e.Amount)
		case "credit":
			sum = sum.Sub(e.Amount)
		default:
			return errors.New("invalid ledger side")
		}
	}
	if !sum.IsZero() {
		return errors.New("unbalanced ledger transaction")
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(digest[:])
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO ledger_transactions(operation_id,actor,currency,fingerprint,original_transaction_id) VALUES ($1,$2,$3,$4,$5) ON CONFLICT(operation_id) DO NOTHING RETURNING id`, operationID, actor, currency, fingerprint, original).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var existingActor, existingCurrency, existingFingerprint string
		var sealed bool
		var existingOriginal sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT actor,currency,fingerprint,sealed,original_transaction_id FROM ledger_transactions WHERE operation_id=$1`, operationID).Scan(&existingActor, &existingCurrency, &existingFingerprint, &sealed, &existingOriginal)
		if err != nil {
			return err
		}
		if existingActor != actor || existingCurrency != currency || existingFingerprint != fingerprint || !sealed || (original == nil && existingOriginal.Valid) || (original != nil && (!existingOriginal.Valid || existingOriginal.Int64 != *original)) {
			return ErrFundingOperationConflict
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("insert ledger transaction: %w", err)
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ledger_entries(transaction_id,account,subject_id,side,amount) VALUES ($1,$2,$3,$4,$5)`, id, e.Account, e.SubjectID, e.Side, e.Amount); err != nil {
			return fmt.Errorf("insert ledger entry: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE ledger_transactions SET sealed=true WHERE id=$1`, id)
	return err
}
