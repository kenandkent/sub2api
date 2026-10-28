package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
)

// UncertainFundingResolution must be invoked only by a strongly authenticated
// administrator after independently checking the provider's final outcome.
type UncertainFundingResolution struct {
	OrderID          int64  `json:"order_id"`
	AttemptID        string `json:"attempt_id"`
	ActorID          int64  `json:"actor_id"`
	Decision         string `json:"decision"`
	ProviderRefundID string `json:"provider_refund_id"`
	Evidence         string `json:"evidence"`
	OperationID      string `json:"operation_id"`
}
type UncertainFundingResolutionResult struct {
	Success  bool   `json:"success"`
	Decision string `json:"decision"`
	Replay   bool   `json:"replay"`
}
type FundingRefundReviewItem struct {
	AttemptID string    `json:"attempt_id"`
	OrderID   int64     `json:"order_id"`
	UserID    int64     `json:"user_id"`
	Amount    float64   `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type paymentFundingReviewRepository interface {
	ResolveUncertainPaymentFunding(context.Context, UncertainFundingResolution, float64, bool) (int64, bool, error)
	ListPendingFundingRefunds(context.Context, int) ([]FundingRefundReviewItem, error)
}

// ListPendingFundingRefunds returns only retained attempts requiring review.
func (s *PaymentService) ListPendingFundingRefunds(ctx context.Context, limit int) ([]FundingRefundReviewItem, error) {
	repo, ok := s.userRepo.(paymentFundingReviewRepository)
	if !ok {
		return nil, errors.New("payment funding review repository unavailable")
	}
	return repo.ListPendingFundingRefunds(ctx, limit)
}

// ResolveUncertainFunding never calls a gateway. The handler must enforce admin
// authorization and step-up authentication and derive ActorID from the session.
func (s *PaymentService) ResolveUncertainFunding(ctx context.Context, cmd UncertainFundingResolution) (*UncertainFundingResolutionResult, error) {
	cmd.AttemptID = strings.TrimSpace(cmd.AttemptID)
	cmd.Decision = strings.TrimSpace(cmd.Decision)
	cmd.ProviderRefundID = strings.TrimSpace(cmd.ProviderRefundID)
	cmd.Evidence = strings.TrimSpace(cmd.Evidence)
	cmd.OperationID = strings.TrimSpace(cmd.OperationID)
	if cmd.OrderID <= 0 || cmd.ActorID <= 0 || cmd.AttemptID == "" || cmd.Evidence == "" || cmd.OperationID == "" || (cmd.Decision != "confirmed_refunded" && cmd.Decision != "confirmed_failed") || (cmd.Decision == "confirmed_refunded" && cmd.ProviderRefundID == "") {
		return nil, errors.New("invalid refund reconciliation evidence or decision")
	}
	repo, ok := s.userRepo.(paymentFundingReviewRepository)
	if !ok {
		return nil, errors.New("payment funding review repository unavailable")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	txCtx := dbent.NewTxContext(ctx, tx)
	order, err := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(cmd.OrderID)).ForUpdate().Only(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock payment order: %w", err)
	}
	allowNew := order.Status == OrderStatusRefunding || order.Status == OrderStatusRefundPending
	user, replay, err := repo.ResolveUncertainPaymentFunding(txCtx, cmd, order.RefundAmount, allowNew)
	if err != nil {
		return nil, err
	}
	if replay {
		return &UncertainFundingResolutionResult{Success: true, Decision: cmd.Decision, Replay: true}, nil
	}
	if user != order.UserID {
		return nil, errors.New("refund reconciliation user mismatch")
	}
	if cmd.Decision == "confirmed_refunded" {
		plan := s.refundFinalizePlan(order)
		plan.fundingAttemptID = cmd.AttemptID
		plan.deductionHeld = true
		if _, err = s.markRefundOkTx(txCtx, tx.Client(), plan); err != nil {
			return nil, err
		}
	} else {
		_, err = tx.PaymentOrder.UpdateOneID(cmd.OrderID).SetStatus(OrderStatusRefundFailed).SetFailedAt(time.Now()).SetFailedReason("provider failure confirmed by review").Save(txCtx)
		if err != nil {
			return nil, err
		}
	}
	if err = createPaymentAuditLogTx(txCtx, tx.Client(), cmd.OrderID, fundingAuditAction("REFUND_REVIEWED"), map[string]any{"attemptID": cmd.AttemptID, "actorID": cmd.ActorID, "decision": cmd.Decision, "providerRefundID": cmd.ProviderRefundID, "evidence": cmd.Evidence, "operationID": cmd.OperationID, "amount": order.RefundAmount}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRefundBalanceCache(ctx, user)
	return &UncertainFundingResolutionResult{Success: true, Decision: cmd.Decision}, nil
}
