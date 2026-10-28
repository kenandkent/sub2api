package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrSupplierFinanceNotFound = errors.New("supplier finance record not found")
var ErrSupplierFinanceInvalid = errors.New("invalid supplier finance request")
var ErrSupplierFinanceConflict = errors.New("supplier finance idempotency conflict")

type FinancePage struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
	Pages    int   `json:"pages"`
}
type FinanceFilter struct {
	Page, PageSize int
	Start, End     *time.Time
	AccountID      int64
	Model          string
}
type SupplierWalletView struct {
	Pending        string    `json:"pending"`
	Available      string    `json:"available"`
	PayoutReserved string    `json:"payout_reserved"`
	Debt           string    `json:"debt"`
	Currency       string    `json:"currency"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type SupplierEarning struct {
	OperationID    string    `json:"operation_id"`
	Date           time.Time `json:"date"`
	AccountID      int64     `json:"account_id"`
	AccountName    string    `json:"account_name"`
	Model          string    `json:"model"`
	ConsumerCharge string    `json:"consumer_charge"`
	PlatformFee    string    `json:"platform_fee"`
	SupplierNet    string    `json:"supplier_net"`
	Status         string    `json:"status"`
	RefundAmount   string    `json:"refund_amount,omitempty"`
}
type SupplierEarningsPage struct {
	FinancePage
	Items  []SupplierEarning  `json:"items"`
	Wallet SupplierWalletView `json:"wallet"`
}
type SupplierStatement struct {
	ID               int64     `json:"id"`
	Month            string    `json:"month"`
	Currency         string    `json:"currency"`
	Version          int       `json:"version"`
	OpeningPending   string    `json:"opening_pending"`
	OpeningAvailable string    `json:"opening_available"`
	OpeningReserved  string    `json:"opening_reserved"`
	OpeningDebt      string    `json:"opening_debt"`
	ClosingPending   string    `json:"closing_pending"`
	ClosingAvailable string    `json:"closing_available"`
	ClosingReserved  string    `json:"closing_reserved"`
	ClosingDebt      string    `json:"closing_debt"`
	DebtOffset       string    `json:"debt_offset"`
	EarnedNet        string    `json:"earned_net"`
	PlatformFee      string    `json:"platform_fee"`
	RefundNet        string    `json:"refund_net"`
	PayoutPaid       string    `json:"payout_paid"`
	CutoffLedgerID   int64     `json:"cutoff_ledger_id"`
	CreatedAt        time.Time `json:"created_at"`
}
type SupplierStatementEntry struct {
	OperationID        string    `json:"operation_id"`
	Date               time.Time `json:"date"`
	AccountID          int64     `json:"account_id"`
	AccountName        string    `json:"account_name"`
	Model              string    `json:"model"`
	Kind               string    `json:"kind"`
	ConsumerCharge     string    `json:"consumer_charge"`
	PlatformFee        string    `json:"platform_fee"`
	SupplierNet        string    `json:"supplier_net"`
	SupplierDebtOffset string    `json:"supplier_debt_offset"`
}
type SupplierStatementEntriesPage struct {
	FinancePage
	Items []SupplierStatementEntry `json:"items"`
}
type SupplierStatementsPage struct {
	FinancePage
	Items []SupplierStatement `json:"items"`
}
type ReconciliationItem struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	AgeSeconds int64  `json:"age_seconds"`
	Difference string `json:"difference,omitempty"`
}
type ReconciliationTotals struct {
	ReceiptStatuses                 map[string]int64 `json:"receipt_statuses"`
	UnbalancedTransactions          int64            `json:"unbalanced_transactions"`
	WalletDifferences               int64            `json:"wallet_differences"`
	FundingDifferences              int64            `json:"funding_differences"`
	BuyerLedgerDifferences          int64            `json:"buyer_ledger_differences"`
	CacheBacklog                    int64            `json:"cache_backlog"`
	CacheOldestAgeSeconds           int64            `json:"cache_oldest_age_seconds"`
	MaturityBacklog                 int64            `json:"maturity_backlog"`
	MaturityOldestAgeSeconds        int64            `json:"maturity_oldest_age_seconds"`
	MaturityFailures                int64            `json:"maturity_failures"`
	MaturityOldestFailureAgeSeconds int64            `json:"maturity_oldest_failure_age_seconds"`
	AgedOutbox                      int64            `json:"aged_outbox"`
	UncertainPayouts                int64            `json:"uncertain_payouts"`
}
type ReconciliationPage struct {
	FinancePage
	Items  []ReconciliationItem `json:"items"`
	Totals ReconciliationTotals `json:"totals"`
}
type MarketplaceAdminReceipt struct {
	ReceiptID        string    `json:"receipt_id"`
	Status           string    `json:"status"`
	ActualCharge     string    `json:"actual_charge"`
	Captured         bool      `json:"captured"`
	RefundedAmount   string    `json:"refunded_amount"`
	RefundableAmount string    `json:"refundable_amount"`
	CaptureStatus    string    `json:"capture_status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type SupplierFinanceStore interface {
	Earnings(context.Context, int64, FinanceFilter) (*SupplierEarningsPage, error)
	Statements(context.Context, int64, int, int) (*SupplierStatementsPage, error)
	Statement(context.Context, int64, int64) (*SupplierStatement, error)
	StatementEntries(context.Context, int64, int64, int, int) (*SupplierStatementEntriesPage, error)
	GenerateClosedMonth(context.Context, int64, time.Time) (*SupplierStatement, error)
	AdminGenerateClosedMonth(context.Context, int64, int64, time.Time, string, string) (*SupplierStatement, error)
	Reconciliation(context.Context, string, int, int) (*ReconciliationPage, error)
}
type SupplierFinanceService struct{ store SupplierFinanceStore }

func NewSupplierFinanceService(store SupplierFinanceStore) *SupplierFinanceService {
	return &SupplierFinanceService{store}
}
func (s *SupplierFinanceService) Earnings(ctx context.Context, id int64, f FinanceFilter) (*SupplierEarningsPage, error) {
	return s.store.Earnings(ctx, id, f)
}
func (s *SupplierFinanceService) Statements(ctx context.Context, id int64, p, n int) (*SupplierStatementsPage, error) {
	return s.store.Statements(ctx, id, p, n)
}
func (s *SupplierFinanceService) Statement(ctx context.Context, id, statement int64) (*SupplierStatement, error) {
	return s.store.Statement(ctx, id, statement)
}
func (s *SupplierFinanceService) StatementEntries(ctx context.Context, owner, statement int64, page, size int) (*SupplierStatementEntriesPage, error) {
	return s.store.StatementEntries(ctx, owner, statement, page, size)
}
func (s *SupplierFinanceService) GenerateClosedMonth(ctx context.Context, id int64, month time.Time) (*SupplierStatement, error) {
	m, err := StatementMonth(month.UTC().Format("2006-01"), time.Now())
	if err != nil || id <= 0 {
		return nil, ErrSupplierFinanceInvalid
	}
	return s.store.GenerateClosedMonth(ctx, id, m)
}
func (s *SupplierFinanceService) AdminGenerateClosedMonth(ctx context.Context, actor, id int64, month, operation, reason string) (*SupplierStatement, error) {
	m, err := StatementMonth(month, time.Now())
	if err != nil || actor <= 0 || id <= 0 || strings.TrimSpace(operation) == "" || len(operation) > 255 || strings.TrimSpace(reason) == "" {
		return nil, ErrSupplierFinanceInvalid
	}
	return s.store.AdminGenerateClosedMonth(ctx, actor, id, m, operation, reason)
}
func (s *SupplierFinanceService) Reconciliation(ctx context.Context, status string, p, n int) (*ReconciliationPage, error) {
	return s.store.Reconciliation(ctx, status, p, n)
}
func (s *SupplierFinanceService) AdminReceipt(ctx context.Context, actor int64, receipt string) (*MarketplaceAdminReceipt, error) {
	store, ok := s.store.(interface {
		AdminReceipt(context.Context, int64, string) (*MarketplaceAdminReceipt, error)
	})
	if !ok || actor <= 0 {
		return nil, ErrSupplierFinanceInvalid
	}
	return store.AdminReceipt(ctx, actor, receipt)
}
func StatementMonth(value string, now time.Time) (time.Time, error) {
	m, err := time.Parse("2006-01", value)
	if err != nil || m.Format("2006-01") != value || !m.AddDate(0, 1, 0).Before(now.UTC()) && !m.AddDate(0, 1, 0).Equal(now.UTC()) {
		return time.Time{}, ErrSupplierFinanceInvalid
	}
	return m, nil
}
func SupplierCSVCell(s string) string {
	stripped := strings.TrimLeft(s, " \t\r\n")
	if len(stripped) > 0 && strings.ContainsRune("=+-@", rune(stripped[0])) {
		return "'" + s
	}
	return s
}
