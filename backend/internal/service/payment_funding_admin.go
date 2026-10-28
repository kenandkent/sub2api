package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Funding administration errors distinguish a proven rollback from an
// idempotency conflict or an outcome that cannot safely be determined.
var (
	ErrFundingAdminRejected      = errors.New("funding administration rejected")
	ErrFundingAdminConflict      = errors.New("funding administration operation conflict")
	ErrFundingRiskStale          = errors.New("funding risk version changed")
	ErrFundingAdminAuthorization = errors.New("funding administration authorization unavailable")
)

type FundingReclassCommand struct {
	UserID, OrderID, ActorID     int64
	Amount                       decimal.Decimal
	Evidence, OperationID        string
	RemainingProvenanceConfirmed bool
}
type FundingReclassResult struct {
	UserID      int64           `json:"user_id"`
	OrderID     int64           `json:"order_id"`
	Amount      decimal.Decimal `json:"amount"`
	CashAmount  decimal.Decimal `json:"cash_amount"`
	BonusAmount decimal.Decimal `json:"bonus_amount"`
	OperationID string          `json:"operation_id"`
	Replay      bool            `json:"replay"`
}
type FundingRisk struct {
	UserID    int64           `json:"user_id"`
	Reason    string          `json:"reason"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
	Debt      decimal.Decimal `json:"debt"`
}
type FundingRiskClearCommand struct {
	UserID, ActorID, ExpectedVersion int64
	Evidence, OperationID            string
}
type FundingRiskClearResult struct {
	UserID      int64  `json:"user_id"`
	Version     int64  `json:"version"`
	OperationID string `json:"operation_id"`
	Replay      bool   `json:"replay"`
}
type fundingAdminRepository interface {
	ReclassifyLegacyFunding(context.Context, FundingReclassCommand) (*FundingReclassResult, error)
	ListFundingRisks(context.Context, int64) ([]FundingRisk, error)
	ClearFundingRisk(context.Context, FundingRiskClearCommand) (*FundingRiskClearResult, error)
}

func (s *PaymentService) ReclassifyLegacyFunding(ctx context.Context, cmd FundingReclassCommand) (*FundingReclassResult, error) {
	cmd.Evidence = strings.TrimSpace(cmd.Evidence)
	cmd.OperationID = strings.TrimSpace(cmd.OperationID)
	if cmd.UserID <= 0 || cmd.OrderID <= 0 || cmd.ActorID <= 0 || !cmd.RemainingProvenanceConfirmed || !cmd.Amount.IsPositive() || !cmd.Amount.Equal(cmd.Amount.Round(8)) || cmd.Evidence == "" || len(cmd.Evidence) > 4000 || cmd.OperationID == "" || len(cmd.OperationID) > 255 {
		return nil, ErrFundingAdminRejected
	}
	repo, ok := s.userRepo.(fundingAdminRepository)
	if !ok {
		return nil, errors.New("funding administration unavailable")
	}
	return repo.ReclassifyLegacyFunding(ctx, cmd)
}
func (s *PaymentService) ListFundingRisks(ctx context.Context, actor int64) ([]FundingRisk, error) {
	repo, ok := s.userRepo.(fundingAdminRepository)
	if !ok || actor <= 0 {
		return nil, errors.New("funding administration unavailable")
	}
	return repo.ListFundingRisks(ctx, actor)
}
func (s *PaymentService) ClearFundingRisk(ctx context.Context, cmd FundingRiskClearCommand) (*FundingRiskClearResult, error) {
	cmd.Evidence = strings.TrimSpace(cmd.Evidence)
	cmd.OperationID = strings.TrimSpace(cmd.OperationID)
	if cmd.UserID <= 0 || cmd.ActorID <= 0 || cmd.ExpectedVersion <= 0 || cmd.Evidence == "" || len(cmd.Evidence) > 4000 || cmd.OperationID == "" || len(cmd.OperationID) > 255 {
		return nil, ErrFundingAdminRejected
	}
	repo, ok := s.userRepo.(fundingAdminRepository)
	if !ok {
		return nil, errors.New("funding administration unavailable")
	}
	return repo.ClearFundingRisk(ctx, cmd)
}
