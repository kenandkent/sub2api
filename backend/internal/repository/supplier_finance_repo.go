package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type SupplierFinanceRepository struct{ db *sql.DB }

func NewSupplierFinanceRepository(db *sql.DB) *SupplierFinanceRepository {
	return &SupplierFinanceRepository{db}
}
func financePage(total int64, p, n int) service.FinancePage {
	pages := int((total + int64(n) - 1) / int64(n))
	if pages < 1 {
		pages = 1
	}
	return service.FinancePage{Total: total, Page: p, PageSize: n, Pages: pages}
}
func validFinancePage(p, n int) bool { return p > 0 && n > 0 && n <= 1000 && p <= 1000000 }
func profileExists(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM supplier_profiles WHERE user_id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return service.ErrSupplierFinanceNotFound
	}
	return nil
}
func (r *SupplierFinanceRepository) Earnings(ctx context.Context, id int64, f service.FinanceFilter) (*service.SupplierEarningsPage, error) {
	if id <= 0 || !validFinancePage(f.Page, f.PageSize) || f.AccountID < 0 {
		return nil, service.ErrSupplierFinanceInvalid
	}
	if err := profileExists(ctx, r.db, id); err != nil {
		return nil, err
	}
	result := &service.SupplierEarningsPage{Items: []service.SupplierEarning{}}
	err := r.db.QueryRowContext(ctx, `SELECT coalesce(w.pending,0)::text,coalesce(w.available,0)::text,coalesce(w.payout_reserved,0)::text,coalesce(w.debt,0)::text,'USD',coalesce((SELECT max(t.created_at) FROM ledger_entries e JOIN ledger_transactions t ON t.id=e.transaction_id WHERE e.subject_id=p.user_id AND e.account LIKE 'supplier_%'),w.created_at,p.created_at) FROM supplier_profiles p LEFT JOIN supplier_wallets w ON w.user_id=p.user_id AND w.currency='USD' WHERE p.user_id=$1`, id).Scan(&result.Wallet.Pending, &result.Wallet.Available, &result.Wallet.PayoutReserved, &result.Wallet.Debt, &result.Wallet.Currency, &result.Wallet.UpdatedAt)
	if err != nil {
		return nil, err
	}
	where := ` FROM marketplace_captures c JOIN marketplace_attempts a ON a.id=c.attempt_id JOIN accounts ac ON ac.id=a.account_id LEFT JOIN ledger_transactions t ON t.id=c.ledger_transaction_id WHERE c.owner_user_id=$1 AND ($2::timestamptz IS NULL OR c.created_at >= $2) AND ($3::timestamptz IS NULL OR c.created_at < $3) AND ($4::bigint=0 OR a.account_id=$4) AND ($5::text='' OR a.quote->>'public_model'=$5)`
	args := []any{id, f.Start, f.End, f.AccountID, f.Model}
	var total int64
	if err = r.db.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	result.FinancePage = financePage(total, f.Page, f.PageSize)
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := r.db.QueryContext(ctx, `SELECT coalesce(t.operation_id,'marketplace:capture:'||c.receipt_id::text),c.created_at,a.account_id,ac.name,coalesce(a.quote->>'public_model',''),c.consumer_charge::text,c.platform_fee::text,c.supplier_net::text,c.status,c.refunded_charge::text`+where+` ORDER BY c.created_at DESC,c.id DESC LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v service.SupplierEarning
		if err = rows.Scan(&v.OperationID, &v.Date, &v.AccountID, &v.AccountName, &v.Model, &v.ConsumerCharge, &v.PlatformFee, &v.SupplierNet, &v.Status, &v.RefundAmount); err != nil {
			return nil, err
		}
		result.Items = append(result.Items, v)
	}
	return result, rows.Err()
}

const statementFields = `id,to_char(month,'YYYY-MM'),currency,version,opening_pending::text,opening_available::text,opening_reserved::text,closing_pending::text,closing_available::text,closing_reserved::text,earned_net::text,platform_fee::text,refund_net::text,payout_paid::text,cutoff_ledger_id,created_at`

type financeScanner interface{ Scan(...any) error }

func scanStatement(row financeScanner) (*service.SupplierStatement, error) {
	var s service.SupplierStatement
	err := row.Scan(&s.ID, &s.Month, &s.Currency, &s.Version, &s.OpeningPending, &s.OpeningAvailable, &s.OpeningReserved, &s.ClosingPending, &s.ClosingAvailable, &s.ClosingReserved, &s.EarnedNet, &s.PlatformFee, &s.RefundNet, &s.PayoutPaid, &s.CutoffLedgerID, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierFinanceNotFound
	}
	return &s, err
}
func (r *SupplierFinanceRepository) Statement(ctx context.Context, id, statement int64) (*service.SupplierStatement, error) {
	if id <= 0 || statement <= 0 {
		return nil, service.ErrSupplierFinanceNotFound
	}
	return scanStatement(r.db.QueryRowContext(ctx, `SELECT `+statementFields+` FROM supplier_statements WHERE supplier_id=$1 AND id=$2`, id, statement))
}
func (r *SupplierFinanceRepository) StatementEntries(ctx context.Context, owner, statement int64, page, size int) (*service.SupplierStatementEntriesPage, error) {
	if !validFinancePage(page, size) {
		return nil, service.ErrSupplierFinanceInvalid
	}
	if _, err := r.Statement(ctx, owner, statement); err != nil {
		return nil, err
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM supplier_statement_entries e JOIN supplier_statements s ON s.id=e.statement_id WHERE s.supplier_id=$1 AND s.id=$2`, owner, statement).Scan(&total); err != nil {
		return nil, err
	}
	out := &service.SupplierStatementEntriesPage{FinancePage: financePage(total, page, size), Items: []service.SupplierStatementEntry{}}
	rows, err := r.db.QueryContext(ctx, `SELECT e.operation_id,e.entry_date,e.account_id,e.account_name,e.model,e.kind,e.consumer_charge::text,e.platform_fee::text,e.supplier_net::text FROM supplier_statement_entries e JOIN supplier_statements s ON s.id=e.statement_id WHERE s.supplier_id=$1 AND s.id=$2 ORDER BY e.entry_date,e.id LIMIT $3 OFFSET $4`, owner, statement, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v service.SupplierStatementEntry
		if err = rows.Scan(&v.OperationID, &v.Date, &v.AccountID, &v.AccountName, &v.Model, &v.Kind, &v.ConsumerCharge, &v.PlatformFee, &v.SupplierNet); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, rows.Err()
}
func (r *SupplierFinanceRepository) Statements(ctx context.Context, id int64, p, n int) (*service.SupplierStatementsPage, error) {
	if !validFinancePage(p, n) {
		return nil, service.ErrSupplierFinanceInvalid
	}
	if err := profileExists(ctx, r.db, id); err != nil {
		return nil, err
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM supplier_statements WHERE supplier_id=$1`, id).Scan(&total); err != nil {
		return nil, err
	}
	out := &service.SupplierStatementsPage{FinancePage: financePage(total, p, n), Items: []service.SupplierStatement{}}
	rows, err := r.db.QueryContext(ctx, `SELECT `+statementFields+` FROM supplier_statements WHERE supplier_id=$1 ORDER BY month DESC,version DESC LIMIT $2 OFFSET $3`, id, n, (p-1)*n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanStatement(rows)
		if e != nil {
			return nil, e
		}
		out.Items = append(out.Items, *v)
	}
	return out, rows.Err()
}
func (r *SupplierFinanceRepository) GenerateClosedMonth(ctx context.Context, id int64, month time.Time) (*service.SupplierStatement, error) {
	return r.generateRetry(ctx, 0, id, month, "", "")
}
func (r *SupplierFinanceRepository) AdminGenerateClosedMonth(ctx context.Context, actor, id int64, month time.Time, operation, reason string) (*service.SupplierStatement, error) {
	return r.generateRetry(ctx, actor, id, month, operation, reason)
}
func (r *SupplierFinanceRepository) generateRetry(ctx context.Context, actor, id int64, month time.Time, operation, reason string) (*service.SupplierStatement, error) {
	for attempt := 0; attempt < 3; attempt++ {
		result, err := r.generate(ctx, actor, id, month, operation, reason)
		var pgErr *pq.Error
		if !errors.As(err, &pgErr) || (pgErr.Code != "40001" && pgErr.Code != "23505") {
			return result, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("statement generation conflicted after retries")
}
func (r *SupplierFinanceRepository) generate(ctx context.Context, actor, id int64, month time.Time, operation, reason string) (*service.SupplierStatement, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if id <= 0 || month.Location() != time.UTC || month.Day() != 1 || month.Hour() != 0 || month.Minute() != 0 || month.Second() != 0 || month.Nanosecond() != 0 {
		return nil, service.ErrSupplierFinanceInvalid
	}
	var closed bool
	if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz + interval '1 month' <= clock_timestamp()`, month).Scan(&closed); err != nil {
		return nil, err
	}
	if !closed {
		return nil, service.ErrSupplierFinanceInvalid
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('marketplace-period:'||to_char($1::timestamptz AT TIME ZONE 'UTC','YYYY-MM'),0))`, month); err != nil {
		return nil, err
	}
	if actor > 0 {
		if !lockPayoutAdmin(ctx, tx, actor) {
			return nil, service.ErrSupplierFinanceInvalid
		}
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "statement:operation:"+operation); err != nil {
			return nil, err
		}
		var oldActor, oldID, oldStatement int64
		var oldMonth time.Time
		var oldReason string
		err = tx.QueryRowContext(ctx, `SELECT actor_id,supplier_id,month,reason,statement_id FROM supplier_statement_generation_operations WHERE operation_id=$1`, operation).Scan(&oldActor, &oldID, &oldMonth, &oldReason, &oldStatement)
		if err == nil {
			if oldActor != actor || oldID != id || !oldMonth.Equal(month) || oldReason != reason {
				return nil, service.ErrSupplierFinanceConflict
			}
			s, e := scanStatement(tx.QueryRowContext(ctx, `SELECT `+statementFields+` FROM supplier_statements WHERE id=$1 AND supplier_id=$2`, oldStatement, id))
			if e != nil {
				return nil, e
			}
			return s, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if err = profileExists(ctx, tx, id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("statement:%d:%s", id, month.Format("2006-01"))); err != nil {
		return nil, err
	}
	s, err := scanStatement(tx.QueryRowContext(ctx, `SELECT `+statementFields+` FROM supplier_statements WHERE supplier_id=$1 AND month=$2`, id, month))
	if errors.Is(err, service.ErrSupplierFinanceNotFound) {
		// All aggregates are tied to the immutable ledger cutoff and UTC transaction time.
		var cutoff int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(id),0) FROM ledger_transactions WHERE sealed`).Scan(&cutoff); err != nil {
			return nil, err
		}
		s, err = scanStatement(tx.QueryRowContext(ctx, `INSERT INTO supplier_statements(supplier_id,month,version,cutoff_ledger_id,opening_pending,opening_available,opening_reserved,closing_pending,closing_available,closing_reserved,earned_net,platform_fee,refund_net,payout_paid)
 SELECT $1,($2::timestamptz AT TIME ZONE 'UTC')::date,1,$3,
 coalesce(sum(delta) FILTER(WHERE account='supplier_pending' AND created_at<$2::timestamptz),0),coalesce(sum(delta) FILTER(WHERE account='supplier_available' AND created_at<$2::timestamptz),0),coalesce(sum(delta) FILTER(WHERE account='supplier_payout_reserved' AND created_at<$2::timestamptz),0),
 coalesce(sum(delta) FILTER(WHERE account='supplier_pending' AND created_at<$4::timestamptz),0),coalesce(sum(delta) FILTER(WHERE account='supplier_available' AND created_at<$4::timestamptz),0),coalesce(sum(delta) FILTER(WHERE account='supplier_payout_reserved' AND created_at<$4::timestamptz),0),
 (SELECT coalesce(sum(c.supplier_net),0) FROM marketplace_captures c JOIN ledger_transactions ct ON ct.id=c.ledger_transaction_id WHERE c.owner_user_id=$1 AND ct.id<=$3 AND ct.booked_at >= $2::timestamptz AND ct.booked_at < $4::timestamptz),
 (SELECT coalesce(sum(c.platform_fee),0) FROM marketplace_captures c JOIN ledger_transactions ct ON ct.id=c.ledger_transaction_id WHERE c.owner_user_id=$1 AND ct.id<=$3 AND ct.booked_at >= $2::timestamptz AND ct.booked_at < $4::timestamptz),
 (SELECT coalesce(sum(f.supplier_amount),0) FROM marketplace_refunds f JOIN marketplace_captures c ON c.receipt_id=f.receipt_id JOIN ledger_transactions ft ON ft.id=f.ledger_transaction_id WHERE c.owner_user_id=$1 AND ft.id<=$3 AND ft.booked_at >= $2::timestamptz AND ft.booked_at < $4::timestamptz),
 coalesce(sum(delta) FILTER(WHERE account='supplier_paid' AND created_at >= $2::timestamptz AND created_at < $4::timestamptz),0)
 FROM (SELECT e.account,t.booked_at AS created_at,CASE e.side WHEN 'credit' THEN e.amount ELSE -e.amount END delta FROM ledger_entries e JOIN ledger_transactions t ON t.id=e.transaction_id WHERE e.subject_id=$1 AND t.sealed AND t.id<=$3) l RETURNING `+statementFields, id, month, cutoff, month.AddDate(0, 1, 0)))

		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO supplier_statement_entries(statement_id,operation_id,entry_date,account_id,account_name,model,kind,consumer_charge,platform_fee,supplier_net)
           SELECT $1::bigint,ct.operation_id,ct.booked_at,a.account_id,ac.name,coalesce(a.quote->>'public_model',''),'capture',c.consumer_charge,c.platform_fee,c.supplier_net
           FROM marketplace_captures c JOIN marketplace_attempts a ON a.id=c.attempt_id JOIN accounts ac ON ac.id=a.account_id JOIN ledger_transactions ct ON ct.id=c.ledger_transaction_id
           WHERE c.owner_user_id=$2 AND ct.id<=$3 AND ct.booked_at>=$4::timestamptz AND ct.booked_at<$5::timestamptz
           UNION ALL
           SELECT $1::bigint,ft.operation_id,ft.booked_at,a.account_id,ac.name,coalesce(a.quote->>'public_model',''),'refund',f.amount,f.platform_amount,f.supplier_amount
           FROM marketplace_refunds f JOIN marketplace_captures c ON c.receipt_id=f.receipt_id JOIN marketplace_attempts a ON a.id=c.attempt_id JOIN accounts ac ON ac.id=a.account_id JOIN ledger_transactions ft ON ft.id=f.ledger_transaction_id
           WHERE c.owner_user_id=$2 AND ft.id<=$3 AND ft.booked_at>=$4::timestamptz AND ft.booked_at<$5::timestamptz`, s.ID, id, cutoff, month, month.AddDate(0, 1, 0))
		}
	}
	if err != nil {
		return nil, err
	}
	if actor > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO supplier_statement_generation_operations(operation_id,actor_id,supplier_id,month,reason,statement_id) VALUES($1,$2,$3,$4,$5,$6)`, operation, actor, id, month, reason, s.ID)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}
func (r *SupplierFinanceRepository) Reconciliation(ctx context.Context, status string, p, n int) (*service.ReconciliationPage, error) {
	if !validFinancePage(p, n) {
		return nil, service.ErrSupplierFinanceInvalid
	}
	out := &service.ReconciliationPage{Items: []service.ReconciliationItem{}, Totals: service.ReconciliationTotals{ReceiptStatuses: map[string]int64{}}}
	rows, err := r.db.QueryContext(ctx, `SELECT status,count(*) FROM marketplace_requests GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		var n int64
		if err = rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out.Totals.ReceiptStatuses[s] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	const issues = `WITH issues AS (
 SELECT 'receipt'::text kind,id::text id,status,extract(epoch from (now()-updated_at))::bigint age_seconds,NULL::text difference FROM marketplace_requests WHERE status IN ('usage_pending','needs_review','dispatched')
 UNION ALL SELECT 'ledger',t.id::text,'unbalanced',extract(epoch from (now()-t.created_at))::bigint,coalesce(sum(CASE e.side WHEN 'debit' THEN e.amount ELSE -e.amount END),0)::text FROM ledger_transactions t LEFT JOIN ledger_entries e ON e.transaction_id=t.id GROUP BY t.id HAVING NOT t.sealed OR count(e.id)<2 OR coalesce(sum(CASE e.side WHEN 'debit' THEN e.amount ELSE -e.amount END),0)<>0
 UNION ALL SELECT 'wallet',w.user_id::text,'projection_difference',extract(epoch from (now()-w.created_at))::bigint, (abs(w.pending-coalesce(l.pending,0))+abs(w.available-coalesce(l.available,0))+abs(w.payout_reserved-coalesce(l.reserved,0))+abs(w.debt-coalesce(l.debt,0)))::text FROM supplier_wallets w LEFT JOIN LATERAL (SELECT sum(CASE WHEN e.account='supplier_pending' THEN CASE e.side WHEN 'credit' THEN e.amount ELSE -e.amount END ELSE 0 END) pending, sum(CASE WHEN e.account='supplier_available' THEN CASE e.side WHEN 'credit' THEN e.amount ELSE -e.amount END ELSE 0 END) available, sum(CASE WHEN e.account='supplier_payout_reserved' THEN CASE e.side WHEN 'credit' THEN e.amount ELSE -e.amount END ELSE 0 END) reserved, sum(CASE WHEN e.account='supplier_debt' THEN CASE e.side WHEN 'debit' THEN e.amount ELSE -e.amount END ELSE 0 END) debt FROM ledger_entries e JOIN ledger_transactions t ON t.id=e.transaction_id AND t.sealed WHERE e.subject_id=w.user_id) l ON true WHERE w.pending<>coalesce(l.pending,0) OR w.available<>coalesce(l.available,0) OR w.payout_reserved<>coalesce(l.reserved,0) OR w.debt<>coalesce(l.debt,0)
 UNION ALL SELECT 'funding',u.id::text,'projection_difference',extract(epoch from (now()-u.created_at))::bigint,(coalesce(l.remaining,0)-coalesce(s.debt,0)-u.balance-u.frozen_balance)::text FROM users u JOIN wallet_funding_states s ON s.user_id=u.id LEFT JOIN LATERAL (SELECT sum(remaining) remaining,sum(reserved) reserved FROM wallet_funding_lots WHERE user_id=u.id) l ON true WHERE coalesce(l.remaining,0)-s.debt<>u.balance+u.frozen_balance OR coalesce(l.reserved,0)<>u.frozen_balance
 UNION ALL SELECT 'outbox',id::text,'aged',extract(epoch from (now()-created_at))::bigint,NULL FROM marketplace_outbox WHERE processed_at IS NULL AND created_at < now()-interval '15 minutes'
 UNION ALL SELECT 'payout',id::text,'processing',extract(epoch from (now()-updated_at))::bigint,NULL FROM supplier_payouts WHERE status='processing'
 ) `
	if err = r.db.QueryRowContext(ctx, issues+`SELECT count(*) FILTER(WHERE kind='ledger'),count(*) FILTER(WHERE kind='wallet'),count(*) FILTER(WHERE kind='funding'),count(*) FILTER(WHERE kind='outbox'),count(*) FILTER(WHERE kind='payout'),count(*) FILTER(WHERE $1='' OR status=$1) FROM issues`, status).Scan(&out.Totals.UnbalancedTransactions, &out.Totals.WalletDifferences, &out.Totals.FundingDifferences, &out.Totals.AgedOutbox, &out.Totals.UncertainPayouts, &out.Total); err != nil {
		return nil, err
	}
	out.FinancePage = financePage(out.Total, p, n)
	rows, err = r.db.QueryContext(ctx, issues+`SELECT kind,id,status,age_seconds,difference FROM issues WHERE $1='' OR status=$1 ORDER BY age_seconds DESC,kind,id LIMIT $2 OFFSET $3`, status, n, (p-1)*n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item service.ReconciliationItem
		var diff sql.NullString
		if err = rows.Scan(&item.Kind, &item.ID, &item.Status, &item.AgeSeconds, &diff); err != nil {
			return nil, err
		}
		item.Difference = diff.String
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
