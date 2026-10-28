package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
)

// PaymentFundingRefundRepository runs only inside the caller's Ent transaction.
// A false migrated result permits the existing legacy refund path.
type PaymentFundingRefundRepository interface {
	ReservePaymentRefund(context.Context, int64, int64, float64, string) (bool, error)
	SettlePaymentRefund(context.Context, string, bool) error
	MarkRefundNeedsReview(context.Context, string) error
	BindRefundProviderReference(context.Context, string, string) error
}

// RecordExternalChargeback records a verified bank dispute without reversing
// the earnings of suppliers who have already fulfilled requests.
func (s *PaymentService) RecordExternalChargeback(ctx context.Context, orderID, userID, actorID int64, amount float64, eventID, evidence string) error {
	eventID, evidence = strings.TrimSpace(eventID), strings.TrimSpace(evidence)
	if orderID <= 0 || userID <= 0 || actorID <= 0 || eventID == "" || evidence == "" {
		return errors.New("invalid chargeback evidence")
	}
	repo, ok := s.userRepo.(interface {
		RecordPaymentChargeback(context.Context, int64, int64, int64, float64, string, string) error
	})
	if !ok {
		return errors.New("payment chargeback repository unavailable")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err = repo.RecordPaymentChargeback(txCtx, orderID, userID, actorID, amount, eventID, evidence); err != nil {
		return err
	}
	if err = createPaymentAuditLogTx(txCtx, tx.Client(), orderID, fundingAuditAction("PAYMENT_CHARGEBACK"), map[string]any{"eventID": eventID, "actorID": actorID, "amount": amount, "evidence": evidence}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.invalidateRefundBalanceCache(ctx, userID)
	return nil
}

// RecordExternalChargebackExact records a verified manual event using the
// persisted order owner and an exact credited-balance amount (including its
// proportional bonus). It does not convert a bank amount or contact a provider.
func (s *PaymentService) RecordExternalChargebackExact(ctx context.Context, orderID, actorID int64, amount decimal.Decimal, eventID, evidence string) error {
	eventID, evidence = strings.TrimSpace(eventID), strings.TrimSpace(evidence)
	if orderID <= 0 || actorID <= 0 || !amount.IsPositive() || !amount.Equal(amount.Round(8)) || amount.GreaterThanOrEqual(decimal.NewFromInt(1000000000000)) || eventID == "" || len(eventID) > 255 || evidence == "" || len(evidence) > 4000 {
		return errors.New("invalid chargeback evidence or amount")
	}
	repo, ok := s.userRepo.(interface {
		RecordPaymentChargebackExact(context.Context, int64, int64, int64, decimal.Decimal, string, string) error
	})
	if !ok {
		return errors.New("payment chargeback repository unavailable")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	order, err := tx.PaymentOrder.Get(txCtx, orderID)
	if err != nil {
		return err
	}
	if err = repo.RecordPaymentChargebackExact(txCtx, orderID, order.UserID, actorID, amount, eventID, evidence); err != nil {
		return err
	}
	if err = createPaymentAuditLogTx(txCtx, tx.Client(), orderID, fundingAuditAction("PAYMENT_CHARGEBACK"), map[string]any{"eventID": eventID, "actorID": actorID, "amount": amount.String(), "evidence": evidence}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.invalidateRefundBalanceCache(ctx, order.UserID)
	return nil
}

// Each funding attempt is independently auditable; the legacy audit table has
// a unique (order_id, action) constraint, so source-aware events use a compact
// unique suffix while their full attempt ID remains in the JSON detail.
func fundingAuditAction(base string) string {
	id := uuid.New()
	return base + ":" + base64.RawURLEncoding.EncodeToString(id[:])
}

// RefundAttemptQueryProvider proves that a query result belongs to the exact
// durable attempt even when the provider did not return a refund ID.
type RefundAttemptQueryProvider interface {
	QueryRefundAttempt(context.Context, payment.RefundQueryRequest, string) (*payment.RefundResponse, error)
}

var errRefundOutcomeUnknown = errors.New("refund provider outcome unknown")

func (s *PaymentService) claimRefund(ctx context.Context, p *RefundPlan) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if p == nil || p.Order == nil || p.Order.ID != p.OrderID || math.IsNaN(p.RefundAmount) || math.IsInf(p.RefundAmount, 0) || p.RefundAmount <= 0 {
		return infraerrors.BadRequest("INVALID_AMOUNT", "invalid refund plan")
	}
	current, err := tx.PaymentOrder.Get(txCtx, p.OrderID)
	if err != nil {
		return err
	}
	if current.UserID != p.Order.UserID || p.RefundAmount-current.Amount > paymentAmountToleranceForCurrency(PaymentOrderCurrency(current)) {
		return infraerrors.BadRequest("INVALID_AMOUNT", "invalid refund plan")
	}
	p.Order = current
	p.GatewayAmount = calculateGatewayRefundAmount(current.Amount, current.PayAmount, p.RefundAmount, PaymentOrderCurrency(current))
	c, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusIn(OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed)).SetStatus(OrderStatusRefunding).SetRefundAmount(p.RefundAmount).SetRefundReason(p.Reason).SetForceRefund(p.Force).Save(txCtx)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	if c == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	attempt := ""
	legacyDeducted := false
	if p.Order.OrderType == payment.OrderTypeBalance {
		repo, ok := s.userRepo.(PaymentFundingRefundRepository)
		if !ok {
			return errors.New("payment funding refund repository unavailable")
		}
		id := uuid.NewString()
		migrated, e := repo.ReservePaymentRefund(txCtx, p.OrderID, p.Order.UserID, p.RefundAmount, id)
		if e != nil {
			return fmt.Errorf("reserve payment refund funding: %w", e)
		}
		if migrated {
			attempt = id
			if err := createPaymentAuditLogTx(txCtx, tx.Client(), p.OrderID, fundingAuditAction("REFUND_FUNDING_RESERVED"), map[string]any{"attemptID": id, "amount": p.RefundAmount}); err != nil {
				return err
			}
		} else if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
			rollbackFailed, e := tx.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionEQ("REFUND_ROLLBACK_FAILED")).Exist(txCtx)
			if e != nil {
				return e
			}
			if !rollbackFailed {
				deducted, e := s.deductAvailableBalance(txCtx, p.Order.UserID, p.BalanceToDeduct)
				if e != nil {
					return fmt.Errorf("legacy refund deduction: %w", e)
				}
				p.BalanceToDeduct = deducted
				legacyDeducted = true
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if legacyDeducted {
		p.legacyDeductionHeld = true
		s.invalidateRefundBalanceCache(ctx, p.Order.UserID)
	}
	if attempt != "" {
		p.fundingAttemptID = attempt
		p.deductionHeld = true
		p.DeductionType = payment.DeductionTypeBalance
		p.BalanceToDeduct = p.RefundAmount
		s.invalidateRefundBalanceCache(ctx, p.Order.UserID)
	}
	return nil
}

func (s *PaymentService) settleFundingRefund(ctx context.Context, p *RefundPlan, confirm bool) error {
	repo, ok := s.userRepo.(PaymentFundingRefundRepository)
	if !ok {
		return errors.New("payment funding refund repository unavailable")
	}
	return repo.SettlePaymentRefund(ctx, p.fundingAttemptID, confirm)
}

func (s *PaymentService) bindFundingRefundReference(ctx context.Context, p *RefundPlan) error {
	if p.providerRefundID == "" {
		return nil
	}
	repo, ok := s.userRepo.(PaymentFundingRefundRepository)
	if !ok {
		return errors.New("payment funding refund repository unavailable")
	}
	return repo.BindRefundProviderReference(ctx, p.fundingAttemptID, p.providerRefundID)
}

// fundingRefundSuccess makes terminal confirmation replayable without a second
// provider request. The attempt ID in the success audit binds it to the claim.
func fundingRefundSuccess(ctx context.Context, client *dbent.Client, oid int64, attempt string) (*RefundResult, bool, error) {
	entry, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionHasPrefix("REFUND_SUCCESS:")).Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).First(ctx)
	if dbent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var detail struct {
		AttemptID string  `json:"attemptID"`
		Balance   float64 `json:"balanceDeducted"`
	}
	if err = json.Unmarshal([]byte(entry.Detail), &detail); err != nil {
		return nil, false, err
	}
	if detail.AttemptID == "" || (attempt != "" && detail.AttemptID != attempt) {
		return nil, false, nil
	}
	return &RefundResult{Success: true, BalanceDeducted: detail.Balance}, true, nil
}

func (s *PaymentService) confirmFundingRefund(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	claimed, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding)).SetStatus(OrderStatusRefunding).Save(txCtx)
	if err != nil {
		return nil, err
	}
	if claimed == 0 {
		result, ok, lookupErr := fundingRefundSuccess(txCtx, tx.Client(), p.OrderID, p.fundingAttemptID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if ok {
			return result, nil
		}
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if p.providerRefundID != "" {
		if err = s.bindFundingRefundReference(txCtx, p); err != nil {
			return nil, err
		}
	}
	if err = s.settleFundingRefund(txCtx, p, true); err != nil {
		return nil, err
	}
	result, err := s.markRefundOkTx(txCtx, tx.Client(), p)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PaymentService) releaseFundingRefund(ctx context.Context, p *RefundPlan, cause error) (*RefundResult, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	status := OrderStatusCompleted
	if p.Order.Status == OrderStatusRefundRequested {
		status = OrderStatusRefundRequested
	}
	count, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding)).SetStatus(status).Save(txCtx)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if err = s.settleFundingRefund(txCtx, p, false); err != nil {
		return nil, err
	}
	if err = createPaymentAuditLogTx(txCtx, tx.Client(), p.OrderID, fundingAuditAction("REFUND_GATEWAY_FAILED"), map[string]any{"detail": psErrMsg(cause), "attemptID": p.fundingAttemptID}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRefundBalanceCache(ctx, p.Order.UserID)
	return &RefundResult{Success: false, Warning: "gateway failed: " + psErrMsg(cause) + ", rolled back"}, nil
}

// --- Refund Flow ---

var createPaymentProviderFromInstance = provider.CreateProvider

// getOrderProviderInstance looks up the provider instance that processed this order.
// For legacy orders without provider_instance_id, it resolves only when the
// historical instance is uniquely identifiable from the stored order fields.
func (s *PaymentService) getOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return s.resolveUniqueLegacyOrderProviderInstance(ctx, o)
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, nil
	}
	return s.entClient.PaymentProviderInstance.Get(ctx, instID)
}

// getRefundOrderProviderInstance resolves the provider instance for refund paths.
// Refunds must be pinned to an explicit historical binding, so legacy
// "best-effort" provider guessing is intentionally not allowed here.
func (s *PaymentService) getRefundOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return nil, nil
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("order %d refund provider instance id is invalid: %s", o.ID, instIDStr)
	}
	inst, err := s.entClient.PaymentProviderInstance.Get(ctx, instID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, fmt.Errorf("order %d refund provider instance %s is missing", o.ID, instIDStr)
		}
		return nil, err
	}
	return inst, nil
}

func (s *PaymentService) resolveUniqueLegacyOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	paymentType := payment.GetBasePaymentType(strings.TrimSpace(o.PaymentType))
	providerKey := strings.TrimSpace(psStringValue(o.ProviderKey))
	if providerKey != "" {
		instances, err := s.entClient.PaymentProviderInstance.Query().
			Where(paymentproviderinstance.ProviderKeyEQ(providerKey)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
		if len(matched) == 1 {
			return matched[0], nil
		}
		return nil, nil
	}

	if paymentType == "" {
		return nil, nil
	}

	instances, err := s.entClient.PaymentProviderInstance.Query().
		All(ctx)
	if err != nil {
		return nil, err
	}

	matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
	if len(matched) == 1 {
		return matched[0], nil
	}
	return nil, nil
}

func psFilterLegacyOrderProviderInstances(orderPaymentType string, instances []*dbent.PaymentProviderInstance) []*dbent.PaymentProviderInstance {
	if len(instances) == 0 {
		return nil
	}
	if strings.TrimSpace(orderPaymentType) == "" {
		return instances
	}
	var matched []*dbent.PaymentProviderInstance
	for _, inst := range instances {
		if psLegacyOrderMatchesInstance(orderPaymentType, inst) {
			matched = append(matched, inst)
		}
	}
	return matched
}

func psLegacyOrderMatchesInstance(orderPaymentType string, inst *dbent.PaymentProviderInstance) bool {
	if inst == nil {
		return false
	}

	baseType := payment.GetBasePaymentType(strings.TrimSpace(orderPaymentType))
	instanceProviderKey := strings.TrimSpace(inst.ProviderKey)
	if baseType == "" {
		return false
	}

	if baseType == payment.TypeStripe {
		return instanceProviderKey == payment.TypeStripe
	}
	if instanceProviderKey == payment.TypeStripe {
		return false
	}
	if instanceProviderKey == baseType {
		return true
	}
	return payment.InstanceSupportsType(inst.SupportedTypes, baseType)
}

func (s *PaymentService) RequestRefund(ctx context.Context, oid, uid int64, reason string) error {
	o, err := s.validateRefundRequest(ctx, oid, uid)
	if err != nil {
		return err
	}
	u, err := s.userRepo.GetByID(ctx, o.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u.Balance < o.Amount {
		return infraerrors.BadRequest("BALANCE_NOT_ENOUGH", "refund amount exceeds balance")
	}
	nr := strings.TrimSpace(reason)
	now := time.Now()
	by := fmt.Sprintf("%d", uid)
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.UserIDEQ(uid), paymentorder.StatusEQ(OrderStatusCompleted), paymentorder.OrderTypeEQ(payment.OrderTypeBalance)).SetStatus(OrderStatusRefundRequested).SetRefundRequestedAt(now).SetRefundRequestReason(nr).SetRefundRequestedBy(by).SetRefundAmount(o.Amount).Save(ctx)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if c == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	s.writeAuditLog(ctx, oid, "REFUND_REQUESTED", fmt.Sprintf("user:%d", uid), map[string]any{"amount": o.Amount, "reason": nr})
	return nil
}

func (s *PaymentService) validateRefundRequest(ctx context.Context, oid, uid int64) (*dbent.PaymentOrder, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != uid {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission")
	}
	if o.OrderType != payment.OrderTypeBalance {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "only balance orders can request refund")
	}
	if o.Status != OrderStatusCompleted {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only completed orders can request refund")
	}
	// Check provider instance allows user refund
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil || inst == nil {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.AllowUserRefund {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "user refund is not enabled for this provider")
	}
	return o, nil
}

func (s *PaymentService) PrepareRefund(ctx context.Context, oid int64, amt float64, reason string, force, deduct bool) (*RefundPlan, *RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	// REFUND_PENDING 只能通过 QueryAndFinalizeRefund 收敛：重新发起会重复扣减并向网关重复退款。
	ok := []string{OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed}
	if !psSliceContains(ok, o.Status) {
		return nil, nil, infraerrors.BadRequest("INVALID_STATUS", "order status does not allow refund")
	}
	// Check provider instance allows admin refund
	inst, instErr := s.getRefundOrderProviderInstance(ctx, o)
	if instErr != nil {
		slog.Warn("refund: provider instance lookup failed", "orderID", oid, "error", instErr)
		return nil, nil, infraerrors.InternalServer("PROVIDER_LOOKUP_FAILED", "failed to look up payment provider for this order")
	}
	if inst == nil {
		// Legacy order without provider_instance_id — block refund
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.RefundEnabled {
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not enabled for this provider")
	}
	if math.IsNaN(amt) || math.IsInf(amt, 0) {
		return nil, nil, infraerrors.BadRequest("INVALID_AMOUNT", "invalid refund amount")
	}
	if amt <= 0 {
		amt = o.Amount
	}
	orderCurrency := PaymentOrderCurrency(o)
	if amt-o.Amount > paymentAmountToleranceForCurrency(orderCurrency) {
		return nil, nil, infraerrors.BadRequest("REFUND_AMOUNT_EXCEEDED", "refund amount exceeds recharge")
	}
	ga := calculateGatewayRefundAmount(o.Amount, o.PayAmount, amt, orderCurrency)
	rr := strings.TrimSpace(reason)
	if rr == "" && o.RefundRequestReason != nil {
		rr = *o.RefundRequestReason
	}
	if rr == "" {
		rr = fmt.Sprintf("refund order:%d", o.ID)
	}
	p := &RefundPlan{OrderID: oid, Order: o, RefundAmount: amt, GatewayAmount: ga, Reason: rr, Force: force, DeductBalance: deduct, DeductionType: payment.DeductionTypeNone}
	if deduct {
		if er := s.prepDeduct(ctx, o, p, force); er != nil {
			return nil, er, nil
		}
	}
	return p, nil, nil
}

func (s *PaymentService) prepDeduct(ctx context.Context, o *dbent.PaymentOrder, p *RefundPlan, force bool) *RefundResult {
	if o.OrderType == payment.OrderTypeSubscription {
		p.DeductionType = payment.DeductionTypeSubscription
		if o.SubscriptionGroupID != nil && o.SubscriptionDays != nil {
			p.SubDaysToDeduct = *o.SubscriptionDays
			sub, err := s.subscriptionSvc.GetActiveSubscription(ctx, o.UserID, *o.SubscriptionGroupID)
			if err == nil && sub != nil {
				p.SubscriptionID = sub.ID
			} else if !force {
				return &RefundResult{Success: false, Warning: "cannot find active subscription for deduction, use force", RequireForce: true}
			}
		}
		return nil
	}
	u, err := s.userRepo.GetByID(ctx, o.UserID)
	if err != nil {
		if !force {
			return &RefundResult{Success: false, Warning: "cannot fetch user balance, use force", RequireForce: true}
		}
		return nil
	}
	p.DeductionType = payment.DeductionTypeBalance
	if u.Balance < p.RefundAmount && !force {
		return &RefundResult{Success: false, Warning: "user balance is insufficient for deduction, use force", RequireForce: true}
	}
	p.BalanceToDeduct = math.Max(0, math.Min(p.RefundAmount, u.Balance))
	return nil
}

type availableBalanceDeductor interface {
	DeductAvailableBalance(ctx context.Context, id int64, amount float64) (float64, error)
}

func (s *PaymentService) deductAvailableBalance(ctx context.Context, userID int64, amount float64) (float64, error) {
	repo, ok := s.userRepo.(availableBalanceDeductor)
	if !ok {
		return 0, errors.New("user repository does not support available balance deduction")
	}
	return repo.DeductAvailableBalance(ctx, userID, amount)
}

// invalidateRefundBalanceCache 退款扣减或归还余额后失效计费缓存，否则网关在缓存 TTL 内仍按旧余额放行。
func (s *PaymentService) invalidateRefundBalanceCache(ctx context.Context, userID int64) {
	if s.redeemService == nil {
		return
	}
	s.redeemService.invalidateRedeemCaches(ctx, userID, &RedeemCode{Type: RedeemTypeBalance})
}

func (s *PaymentService) ExecuteRefund(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	if err := s.claimRefund(ctx, p); err != nil {
		return nil, err
	}
	if p.fundingAttemptID == "" && !p.legacyDeductionHeld && p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		// Skip balance deduction on retry if previous attempt already deducted
		// but failed to roll back (REFUND_ROLLBACK_FAILED in audit log).
		if !s.hasAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED") {
			deducted, err := s.deductAvailableBalance(ctx, p.Order.UserID, p.BalanceToDeduct)
			if err != nil {
				s.restoreStatus(ctx, p)
				return nil, fmt.Errorf("deduction: %w", err)
			}
			p.BalanceToDeduct = deducted
			s.invalidateRefundBalanceCache(ctx, p.Order.UserID)
		} else {
			slog.Warn("skipping balance deduction on retry (previous rollback failed)", "orderID", p.OrderID)
			p.BalanceToDeduct = 0
		}
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if !s.hasAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED") {
			_, err := s.subscriptionSvc.ExtendSubscription(ctx, p.SubscriptionID, -p.SubDaysToDeduct)
			if err != nil {
				if errors.Is(err, ErrAdjustWouldExpire) {
					// Deduction would expire the subscription — revoke it entirely
					slog.Info("subscription deduction would expire, revoking", "orderID", p.OrderID, "subID", p.SubscriptionID, "days", p.SubDaysToDeduct)
					if revokeErr := s.subscriptionSvc.RevokeSubscription(ctx, p.SubscriptionID); revokeErr != nil {
						s.restoreStatus(ctx, p)
						return nil, fmt.Errorf("revoke subscription: %w", revokeErr)
					}
				} else {
					// Other errors (DB failure, not found) — abort refund
					s.restoreStatus(ctx, p)
					return nil, fmt.Errorf("deduct subscription days: %w", err)
				}
			}
		} else {
			slog.Warn("skipping subscription deduction on retry (previous rollback failed)", "orderID", p.OrderID)
			p.SubDaysToDeduct = 0
		}
	}
	resp, err := s.gwRefund(ctx, p)
	if err != nil {
		if errors.Is(err, errRefundOutcomeUnknown) {
			return s.markRefundPending(ctx, p, resp)
		}
		return s.handleGwFail(ctx, p, err)
	}
	return s.finishRefund(ctx, p, resp)
}

func (s *PaymentService) gwRefund(ctx context.Context, p *RefundPlan) (*payment.RefundResponse, error) {
	if p.Order.PaymentTradeNo == "" {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_NO_TRADE_NO", "admin", map[string]any{"detail": "skipped"})
		return &payment.RefundResponse{Status: payment.ProviderStatusSuccess}, nil
	}

	// Use the exact provider instance that created this order, not a random one
	// from the registry. Each instance has its own merchant credentials.
	prov, err := s.getRefundProvider(ctx, p.Order)
	if err != nil {
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	if err := validateProviderSnapshotMetadata(p.Order, prov.ProviderKey(), providerMerchantIdentityMetadata(prov)); err != nil {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_PROVIDER_METADATA_MISMATCH", "admin", map[string]any{
			"detail": err.Error(),
		})
		return nil, err
	}
	finishProviderCall := servertiming.ObserveDependency(ctx, "payment")
	resp, err := prov.Refund(ctx, payment.RefundRequest{
		TradeNo: p.Order.PaymentTradeNo,
		OrderID: p.Order.OutTradeNo,
		Amount:  formatGatewayRefundAmount(p.GatewayAmount, p.Order),
		Reason:  p.Reason,
	})
	finishProviderCall()
	if err != nil {
		if resp != nil && strings.TrimSpace(resp.Status) == payment.ProviderStatusFailed {
			return resp, err
		}
		return resp, fmt.Errorf("%w: %v", errRefundOutcomeUnknown, err)
	}
	if resp == nil {
		return nil, errRefundOutcomeUnknown
	}
	if err := validateRefundProviderResponse(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func formatGatewayRefundAmount(amount float64, order *dbent.PaymentOrder) string {
	return payment.FormatAmountForCurrency(amount, PaymentOrderCurrency(order))
}

func validateRefundProviderResponse(resp *payment.RefundResponse) error {
	if resp == nil {
		return fmt.Errorf("payment refund response missing")
	}
	status := strings.TrimSpace(resp.Status)
	switch status {
	case payment.ProviderStatusFailed:
		return fmt.Errorf("payment refund failed: status %s", status)
	default:
		// An unrecognized response is not proof that the provider rejected it.
		return nil
	}
}

func (s *PaymentService) finishRefund(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (*RefundResult, error) {
	if resp != nil && p.fundingAttemptID != "" {
		p.providerRefundID = strings.TrimSpace(resp.RefundID)
	}
	if err := validateRefundProviderResponse(resp); err != nil {
		return s.handleGwFail(ctx, p, err)
	}
	switch strings.TrimSpace(resp.Status) {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		if p.fundingAttemptID != "" {
			return s.confirmFundingRefund(ctx, p)
		}
		return s.markRefundOk(ctx, p)
	case payment.ProviderStatusPending:
		return s.markRefundPending(ctx, p, resp)
	default:
		return s.markRefundPending(ctx, p, resp)
	}
}

func (s *PaymentService) QueryAndFinalizeRefund(ctx context.Context, oid int64) (*RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	recovering := o.Status == OrderStatusRefunding
	if o.Status == OrderStatusRefunded || o.Status == OrderStatusPartiallyRefunded {
		result, ok, err := fundingRefundSuccess(ctx, s.entClient, oid, "")
		if err != nil {
			return nil, err
		}
		if ok {
			return result, nil
		}
	}
	if o.Status != OrderStatusRefundPending && !recovering {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only refund pending or refunding orders can be finalized")
	}

	var pendingDetail refundPendingAuditDetail
	if recovering {
		pendingDetail, err = s.latestRefundFundingClaim(ctx, oid)
	} else {
		pendingDetail, err = s.latestRefundPendingDetail(ctx, oid)
	}
	if err != nil {
		return nil, err
	}
	prov, err := s.getRefundProvider(ctx, o)
	if err != nil {
		if pendingDetail.FundingAttemptID != "" {
			if reviewErr := s.markRefundNeedsReview(ctx, oid, pendingDetail.FundingAttemptID, "provider unavailable: "+err.Error()); reviewErr != nil {
				return nil, reviewErr
			}
		}
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	queryProvider, ok := prov.(payment.RefundQueryProvider)
	if !ok {
		if pendingDetail.FundingAttemptID != "" {
			if reviewErr := s.markRefundNeedsReview(ctx, oid, pendingDetail.FundingAttemptID, "provider does not support refund query"); reviewErr != nil {
				return nil, reviewErr
			}
		}
		return nil, infraerrors.BadRequest("REFUND_QUERY_UNSUPPORTED", "this payment provider does not support refund status query; please verify manually")
	}

	if recovering && math.Abs(pendingDetail.RefundAmount-o.RefundAmount) > paymentAmountToleranceForCurrency(PaymentOrderCurrency(o)) {
		return nil, errors.New("refund claim amount mismatch")
	}
	request := payment.RefundQueryRequest{
		TradeNo: o.PaymentTradeNo, OrderID: o.OutTradeNo, RefundID: pendingDetail.RefundID,
		Amount:      formatGatewayRefundAmount(calculateGatewayRefundAmount(o.Amount, o.PayAmount, o.RefundAmount, PaymentOrderCurrency(o)), o),
		RequestedAt: pendingDetail.requestedAt,
	}
	needsProof := pendingDetail.FundingAttemptID != "" && strings.TrimSpace(pendingDetail.RefundID) == ""
	proofProvider, hasProof := prov.(RefundAttemptQueryProvider)
	finishProviderCall := servertiming.ObserveDependency(ctx, "payment")
	var resp *payment.RefundResponse
	if needsProof && hasProof {
		resp, err = proofProvider.QueryRefundAttempt(ctx, request, pendingDetail.FundingAttemptID)
	} else {
		resp, err = queryProvider.QueryRefund(ctx, request)
	}
	finishProviderCall()
	if err != nil {
		if recovering || needsProof {
			if reviewErr := s.markRefundNeedsReview(ctx, oid, pendingDetail.FundingAttemptID, "provider query failed: "+err.Error()); reviewErr != nil {
				return nil, reviewErr
			}
		}
		return nil, fmt.Errorf("query refund: %w", err)
	}
	if pendingDetail.FundingAttemptID != "" && pendingDetail.RefundID != "" && resp != nil && strings.TrimSpace(resp.RefundID) != strings.TrimSpace(pendingDetail.RefundID) {
		if err := s.markRefundNeedsReview(ctx, oid, pendingDetail.FundingAttemptID, "provider refund reference mismatch"); err != nil {
			return nil, err
		}
		return &RefundResult{Success: false, Warning: "refund needs manual review; funds remain held"}, nil
	}
	if needsProof && !hasProof {
		if err := s.markRefundNeedsReview(ctx, oid, pendingDetail.FundingAttemptID, "provider query cannot prove the refund attempt"); err != nil {
			return nil, err
		}
		return &RefundResult{Success: false, Warning: "refund needs manual review; funds remain held"}, nil
	}
	plan := s.refundFinalizePlan(o)
	switch {
	case pendingDetail.DeductionHeld:
		pendingDetail.applyTo(plan)
	case !pendingDetail.DeductionRollbackOK:
		plan.BalanceToDeduct = 0
		plan.SubDaysToDeduct = 0
	case o.OrderType == payment.OrderTypeSubscription:
		if early := s.prepDeduct(ctx, o, plan, true); early != nil {
			return early, nil
		}
	}
	if resp != nil && plan.fundingAttemptID != "" {
		plan.providerRefundID = strings.TrimSpace(resp.RefundID)
	}
	if recovering {
		if resp != nil && (strings.TrimSpace(resp.Status) == payment.ProviderStatusSuccess || strings.TrimSpace(resp.Status) == payment.ProviderStatusRefunded) {
			return s.confirmFundingRefund(ctx, plan)
		}
		status := "missing response"
		if resp != nil {
			status = strings.TrimSpace(resp.Status)
		}
		if err := s.markRefundNeedsReview(ctx, oid, plan.fundingAttemptID, "provider query inconclusive: "+status); err != nil {
			return nil, err
		}
		return &RefundResult{Success: false, Warning: "refund needs manual review; funds remain held"}, nil
	}
	if resp == nil {
		return &RefundResult{Success: false, Warning: "gateway refund is still pending confirmation"}, nil
	}
	if err := validateRefundProviderResponse(resp); err != nil {
		return s.finalizeRefundFailed(ctx, plan, err)
	}
	switch strings.TrimSpace(resp.Status) {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return s.finalizePendingRefundSuccess(ctx, plan)
	case payment.ProviderStatusPending:
		s.writeAuditLog(ctx, oid, "REFUND_QUERY_PENDING", "admin", map[string]any{"refundID": resp.RefundID})
		return &RefundResult{Success: false, Warning: "gateway refund is still pending confirmation"}, nil
	default:
		return &RefundResult{Success: false, Warning: "gateway refund is still pending confirmation"}, nil
	}
}

func (s *PaymentService) finalizePendingRefundSuccess(ctx context.Context, p *RefundPlan) (_ *RefundResult, err error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	claimed, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefundPending)).
		SetStatus(OrderStatusRefunding).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("claim pending refund: %w", err)
	}
	if claimed == 0 {
		if p.fundingAttemptID != "" {
			result, ok, lookupErr := fundingRefundSuccess(txCtx, tx.Client(), p.OrderID, p.fundingAttemptID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if ok {
				return result, nil
			}
		}
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}

	if p.fundingAttemptID != "" {
		if p.providerRefundID != "" {
			if err := s.bindFundingRefundReference(txCtx, p); err != nil {
				return nil, err
			}
		}
		if err := s.settleFundingRefund(txCtx, p, true); err != nil {
			return nil, err
		}
	} else if err := s.applyRefundFinalDeduction(txCtx, p); err != nil {
		return nil, err
	}
	result, err := s.markRefundOkTx(txCtx, tx.Client(), p)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit refund finalization: %w", err)
	}
	if !p.deductionHeld && p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		s.invalidateRefundBalanceCache(ctx, p.Order.UserID)
	}
	return result, nil
}

func (s *PaymentService) refundFinalizePlan(o *dbent.PaymentOrder) *RefundPlan {
	refundAmount := o.RefundAmount
	reason := strings.TrimSpace(psStringValue(o.RefundReason))
	if reason == "" {
		reason = fmt.Sprintf("refund order:%d", o.ID)
	}
	return &RefundPlan{
		OrderID:       o.ID,
		Order:         o,
		RefundAmount:  refundAmount,
		GatewayAmount: calculateGatewayRefundAmount(o.Amount, o.PayAmount, refundAmount, PaymentOrderCurrency(o)),
		Reason:        reason,
		Force:         o.ForceRefund,
		DeductBalance: true,
		DeductionType: payment.DeductionTypeBalance,
		BalanceToDeduct: func() float64 {
			if o.OrderType == payment.OrderTypeBalance {
				return refundAmount
			}
			return 0
		}(),
	}
}

func (s *PaymentService) applyRefundFinalDeduction(ctx context.Context, p *RefundPlan) error {
	if p.deductionHeld {
		// 进入 pending 时扣减已保留，终态成功无需再扣。
		return nil
	}
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		deducted, err := s.deductAvailableBalance(ctx, p.Order.UserID, p.BalanceToDeduct)
		if err != nil {
			return fmt.Errorf("deduction: %w", err)
		}
		p.BalanceToDeduct = deducted
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if _, err := s.subscriptionSvc.ExtendSubscription(ctx, p.SubscriptionID, -p.SubDaysToDeduct); err != nil {
			if errors.Is(err, ErrAdjustWouldExpire) {
				if revokeErr := s.subscriptionSvc.RevokeSubscription(ctx, p.SubscriptionID); revokeErr != nil {
					return fmt.Errorf("revoke subscription: %w", revokeErr)
				}
			} else {
				return fmt.Errorf("deduct subscription days: %w", err)
			}
		}
	}
	return nil
}

// finalizeRefundFailed 将 REFUND_PENDING 收敛为 REFUND_FAILED，并归还进入 pending 时保留的扣减。
// 认领状态、归还扣减与审计在同一事务内完成：两者分开提交时，进程在中间退出会留下
// 已失败但扣减未归还、且没有 REFUND_ROLLBACK_FAILED 标记的订单，重试退款会再扣一次。
func (s *PaymentService) finalizeRefundFailed(ctx context.Context, p *RefundPlan, gErr error) (*RefundResult, error) {
	warning := "gateway refund failed: " + psErrMsg(gErr)
	returnErr, err := s.finalizeRefundFailedTx(ctx, p, gErr, nil)
	if err != nil {
		return nil, err
	}
	if returnErr == nil {
		if p.deductionHeld && p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
			s.invalidateRefundBalanceCache(ctx, p.Order.UserID)
		}
		return &RefundResult{Success: false, Warning: warning}, nil
	}

	if p.fundingAttemptID != "" && returnErr != nil {
		return nil, returnErr
	}
	// 归还失败（整个事务已回滚，订单仍为 REFUND_PENDING）：仍收敛为 REFUND_FAILED，
	// 并同事务写入 REFUND_ROLLBACK_FAILED，使重试退款跳过扣减，避免二次扣减。
	slog.Error("[CRITICAL] refund failed but held deduction could not be returned", "orderID", p.OrderID, "error", returnErr)
	if _, err := s.finalizeRefundFailedTx(ctx, p, gErr, returnErr); err != nil {
		return nil, err
	}
	return &RefundResult{Success: false, Warning: warning + "; refund deduction rollback failed"}, nil
}

// finalizeRefundFailedTx 在单个事务内认领 REFUND_PENDING → REFUND_FAILED。rollbackErr 为 nil 时同事务
// 归还保留的扣减，归还失败则回滚整个事务并通过 returnErr 返回；rollbackErr 非 nil 时不再归还，
// 改为同事务记录 REFUND_ROLLBACK_FAILED。
func (s *PaymentService) finalizeRefundFailedTx(ctx context.Context, p *RefundPlan, gErr, rollbackErr error) (returnErr error, err error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund failure finalization: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)

	claimed, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefundPending)).
		SetStatus(OrderStatusRefundFailed).
		SetFailedAt(time.Now()).
		SetFailedReason(psErrMsg(gErr)).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("mark refund failed: %w", err)
	}
	if claimed == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}

	if p.deductionHeld && rollbackErr == nil {
		if p.fundingAttemptID != "" {
			if err := s.settleFundingRefund(txCtx, p, false); err != nil {
				return err, nil
			}
		} else if err := s.returnHeldRefundDeduction(txCtx, p); err != nil {
			return err, nil
		}
	}
	if rollbackErr != nil {
		if err := createPaymentAuditLogTx(txCtx, tx.Client(), p.OrderID, "REFUND_ROLLBACK_FAILED", map[string]any{
			"gatewayError":    psErrMsg(gErr),
			"rollbackError":   psErrMsg(rollbackErr),
			"balanceDeducted": p.BalanceToDeduct,
			"subDaysDeducted": p.SubDaysToDeduct,
		}); err != nil {
			return nil, err
		}
	}
	failedAction := "REFUND_FAILED"
	if p.fundingAttemptID != "" {
		failedAction = fundingAuditAction(failedAction)
	}
	if err := createPaymentAuditLogTx(txCtx, tx.Client(), p.OrderID, failedAction, map[string]any{"detail": psErrMsg(gErr), "attemptID": p.fundingAttemptID}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit refund failure finalization: %w", err)
	}
	committed = true
	return nil, nil
}

// returnHeldRefundDeduction 归还进入 REFUND_PENDING 时保留的扣减；ctx 携带事务时随事务提交或回滚。
func (s *PaymentService) returnHeldRefundDeduction(ctx context.Context, p *RefundPlan) error {
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		if err := s.userRepo.UpdateBalance(ctx, p.Order.UserID, p.BalanceToDeduct); err != nil {
			return fmt.Errorf("return held balance deduction: %w", err)
		}
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if _, err := s.subscriptionSvc.ExtendSubscription(ctx, p.SubscriptionID, p.SubDaysToDeduct); err != nil {
			return fmt.Errorf("return held subscription deduction: %w", err)
		}
	}
	return nil
}

func createPaymentAuditLogTx(ctx context.Context, client *dbent.Client, oid int64, action string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal %s audit: %w", action, err)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(oid, 10)).
		SetAction(action).
		SetDetail(string(raw)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return fmt.Errorf("write %s audit: %w", action, err)
	}
	return nil
}

// refundPendingAuditDetail 记录在 REFUND_PENDING 审计日志中。DeductionHeld 为 true 表示
// 进入 pending 时扣减被保留；旧记录没有该字段，扣减已在 pending 时归还（DeductionRollbackOK）。
type refundPendingAuditDetail struct {
	RefundID            string  `json:"refundID"`
	FundingAttemptID    string  `json:"fundingAttemptID,omitempty"`
	RefundAmount        float64 `json:"refundAmount"`
	Reason              string  `json:"reason"`
	Force               bool    `json:"force"`
	DeductionRollbackOK bool    `json:"deductionRollbackOK,omitempty"`
	DeductionHeld       bool    `json:"deductionHeld,omitempty"`
	DeductionType       string  `json:"deductionType,omitempty"`
	BalanceDeducted     float64 `json:"balanceDeducted"`
	SubDaysDeducted     int     `json:"subDaysDeducted"`
	SubscriptionID      int64   `json:"subscriptionID,omitempty"`
	// requestedAt 取自审计记录的创建时间，不参与序列化。
	requestedAt time.Time
}

func (d refundPendingAuditDetail) applyTo(p *RefundPlan) {
	p.deductionHeld = true
	p.fundingAttemptID = d.FundingAttemptID
	p.DeductionType = d.DeductionType
	if p.DeductionType == "" {
		p.DeductionType = payment.DeductionTypeNone
	}
	p.BalanceToDeduct = d.BalanceDeducted
	p.SubDaysToDeduct = d.SubDaysDeducted
	p.SubscriptionID = d.SubscriptionID
}

// A claimed attempt can be queried after a crash, but an inconclusive query
// cannot release funds: the original provider call may still be in flight.
func (s *PaymentService) latestRefundFundingClaim(ctx context.Context, oid int64) (refundPendingAuditDetail, error) {
	logEntry, err := s.entClient.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.Or(paymentauditlog.ActionEQ("REFUND_FUNDING_RESERVED"), paymentauditlog.ActionHasPrefix("REFUND_FUNDING_RESERVED:"))).Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).First(ctx)
	if err != nil {
		return refundPendingAuditDetail{}, fmt.Errorf("load refund funding claim: %w", err)
	}
	var claim struct {
		AttemptID string  `json:"attemptID"`
		Amount    float64 `json:"amount"`
	}
	if err = json.Unmarshal([]byte(logEntry.Detail), &claim); err != nil {
		return refundPendingAuditDetail{}, err
	}
	if strings.TrimSpace(claim.AttemptID) == "" || claim.Amount <= 0 {
		return refundPendingAuditDetail{}, errors.New("invalid refund funding claim")
	}
	return refundPendingAuditDetail{FundingAttemptID: claim.AttemptID, RefundAmount: claim.Amount, DeductionHeld: true, DeductionType: payment.DeductionTypeBalance, BalanceDeducted: claim.Amount, requestedAt: logEntry.CreatedAt}, nil
}

func (s *PaymentService) markRefundNeedsReview(ctx context.Context, oid int64, attempt, reason string) error {
	repo, ok := s.userRepo.(PaymentFundingRefundRepository)
	if !ok {
		return errors.New("payment funding refund repository unavailable")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	current, err := tx.PaymentOrder.Get(txCtx, oid)
	if err != nil {
		return err
	}
	if current.Status != OrderStatusRefunding && current.Status != OrderStatusRefundPending {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	count, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.StatusEQ(current.Status)).SetStatus(current.Status).Save(txCtx)
	if err != nil {
		return err
	}
	if count == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if err = repo.MarkRefundNeedsReview(txCtx, attempt); err != nil {
		return err
	}
	if err = createPaymentAuditLogTx(txCtx, tx.Client(), oid, fundingAuditAction("REFUND_NEEDS_REVIEW"), map[string]any{"attemptID": attempt, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

// latestRefundPendingDetail 读取最近一次 REFUND_PENDING 审计。没有记录视为旧订单（扣减已在
// pending 时归还）；读取或解析失败必须返回错误：误判为旧记录会让保留的扣减被重复扣减或不再归还。
func (s *PaymentService) latestRefundPendingDetail(ctx context.Context, oid int64) (refundPendingAuditDetail, error) {
	detail := refundPendingAuditDetail{DeductionRollbackOK: true}
	logEntry, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.Or(paymentauditlog.ActionEQ("REFUND_PENDING"), paymentauditlog.ActionHasPrefix("REFUND_PENDING:"))).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc()), paymentauditlog.ByID(sql.OrderDesc())).
		First(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return detail, nil
		}
		return refundPendingAuditDetail{}, fmt.Errorf("load refund pending audit: %w", err)
	}
	if err := json.Unmarshal([]byte(logEntry.Detail), &detail); err != nil {
		return refundPendingAuditDetail{}, fmt.Errorf("parse refund pending audit: %w", err)
	}
	detail.RefundID = strings.TrimSpace(detail.RefundID)
	detail.requestedAt = logEntry.CreatedAt
	return detail, nil
}

// getRefundProvider creates a provider using the order's original instance config.
// Delegates to getOrderProvider which handles instance lookup and fallback.
func (s *PaymentService) getRefundProvider(ctx context.Context, o *dbent.PaymentOrder) (payment.Provider, error) {
	if s.refundProviderResolver != nil {
		return s.refundProviderResolver(ctx, o)
	}
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("refund provider instance is unavailable for order %d", o.ID)
	}
	return s.createProviderFromInstance(ctx, inst)
}

func (s *PaymentService) handleGwFail(ctx context.Context, p *RefundPlan, gErr error) (*RefundResult, error) {
	if p.fundingAttemptID != "" {
		return s.releaseFundingRefund(ctx, p, gErr)
	}
	if s.RollbackRefund(ctx, p, gErr) {
		s.restoreStatus(ctx, p)
		s.writeAuditLog(ctx, p.OrderID, "REFUND_GATEWAY_FAILED", "admin", map[string]any{"detail": psErrMsg(gErr)})
		return &RefundResult{Success: false, Warning: "gateway failed: " + psErrMsg(gErr) + ", rolled back"}, nil
	}
	now := time.Now()
	_, _ = s.entClient.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(OrderStatusRefundFailed).SetFailedAt(now).SetFailedReason(psErrMsg(gErr)).Save(ctx)
	s.writeAuditLog(ctx, p.OrderID, "REFUND_FAILED", "admin", map[string]any{"detail": psErrMsg(gErr)})
	return nil, infraerrors.InternalServer("REFUND_FAILED", psErrMsg(gErr))
}

func (s *PaymentService) markRefundOk(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	fs := OrderStatusRefunded
	if p.RefundAmount < p.Order.Amount {
		fs = OrderStatusPartiallyRefunded
	}
	now := time.Now()
	_, err := s.entClient.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(fs).SetRefundAmount(p.RefundAmount).SetRefundReason(p.Reason).SetRefundAt(now).SetForceRefund(p.Force).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund: %w", err)
	}
	s.writeAuditLog(ctx, p.OrderID, "REFUND_SUCCESS", "admin", map[string]any{"refundAmount": p.RefundAmount, "reason": p.Reason, "balanceDeducted": p.BalanceToDeduct, "force": p.Force})
	return &RefundResult{Success: true, BalanceDeducted: p.BalanceToDeduct, SubDaysDeducted: p.SubDaysToDeduct}, nil
}

func (s *PaymentService) markRefundOkTx(ctx context.Context, client *dbent.Client, p *RefundPlan) (*RefundResult, error) {
	fs := OrderStatusRefunded
	if p.RefundAmount < p.Order.Amount {
		fs = OrderStatusPartiallyRefunded
	}
	now := time.Now()
	_, err := client.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(fs).SetRefundAmount(p.RefundAmount).SetRefundReason(p.Reason).SetRefundAt(now).SetForceRefund(p.Force).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund: %w", err)
	}
	detail, err := json.Marshal(map[string]any{"refundAmount": p.RefundAmount, "reason": p.Reason, "balanceDeducted": p.BalanceToDeduct, "force": p.Force, "attemptID": p.fundingAttemptID})
	if err != nil {
		return nil, fmt.Errorf("marshal refund audit: %w", err)
	}
	action := "REFUND_SUCCESS"
	if p.fundingAttemptID != "" {
		action = fundingAuditAction(action)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction(action).
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return nil, fmt.Errorf("write refund audit: %w", err)
	}
	return &RefundResult{Success: true, BalanceDeducted: p.BalanceToDeduct, SubDaysDeducted: p.SubDaysToDeduct}, nil
}

// markRefundPending 在网关未给出终态时保留已执行的扣减：若此时归还，用户可在 pending 期间
// 消费这部分余额，终态成功时只能扣到剩余余额。网关确认失败后由 finalizeRefundFailed 归还。
// 订单状态与审计记录同事务写入，确保终态收敛时能读到保留的扣减。
func (s *PaymentService) markRefundPending(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (_ *RefundResult, err error) {
	detail, err := json.Marshal(refundPendingAuditDetail{
		RefundID:         refundResponseID(resp),
		FundingAttemptID: p.fundingAttemptID,
		RefundAmount:     p.RefundAmount,
		Reason:           p.Reason,
		Force:            p.Force,
		DeductionHeld:    true,
		DeductionType:    p.DeductionType,
		BalanceDeducted:  p.BalanceToDeduct,
		SubDaysDeducted:  p.SubDaysToDeduct,
		SubscriptionID:   p.SubscriptionID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal refund pending audit: %w", err)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin mark refund pending: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
			slog.Error("[CRITICAL] gateway refund pending but order not marked", "orderID", p.OrderID, "error", err)
		}
	}()
	count, err := tx.PaymentOrder.Update().Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding)).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		ClearRefundAt().
		SetForceRefund(p.Force).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund pending: %w", err)
	}
	if count == 0 {
		if p.fundingAttemptID != "" {
			result, ok, lookupErr := fundingRefundSuccess(dbent.NewTxContext(ctx, tx), tx.Client(), p.OrderID, p.fundingAttemptID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if ok {
				_ = tx.Rollback()
				return result, nil
			}
		}
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if p.fundingAttemptID != "" && refundResponseID(resp) != "" {
		repo, ok := s.userRepo.(PaymentFundingRefundRepository)
		if !ok {
			return nil, errors.New("payment funding refund repository unavailable")
		}
		if err = repo.BindRefundProviderReference(dbent.NewTxContext(ctx, tx), p.fundingAttemptID, refundResponseID(resp)); err != nil {
			return nil, err
		}
	}
	if p.fundingAttemptID != "" && (resp == nil || strings.TrimSpace(resp.Status) != payment.ProviderStatusPending || refundResponseID(resp) == "") {
		repo, ok := s.userRepo.(PaymentFundingRefundRepository)
		if !ok {
			return nil, errors.New("payment funding refund repository unavailable")
		}
		txCtx := dbent.NewTxContext(ctx, tx)
		if err = repo.MarkRefundNeedsReview(txCtx, p.fundingAttemptID); err != nil {
			return nil, err
		}
		if err = createPaymentAuditLogTx(txCtx, tx.Client(), p.OrderID, fundingAuditAction("REFUND_NEEDS_REVIEW"), map[string]any{"attemptID": p.fundingAttemptID, "reason": "provider outcome or refund reference unknown"}); err != nil {
			return nil, err
		}
	}
	action := "REFUND_PENDING"
	if p.fundingAttemptID != "" {
		action = fundingAuditAction(action)
	}
	if _, err = tx.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction(action).
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return nil, fmt.Errorf("write refund pending audit: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit mark refund pending: %w", err)
	}
	return &RefundResult{Success: false, Warning: "gateway refund is pending confirmation"}, nil
}

func refundResponseID(resp *payment.RefundResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.RefundID)
}

func (s *PaymentService) RollbackRefund(ctx context.Context, p *RefundPlan, gErr error) bool {
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		if err := s.userRepo.UpdateBalance(ctx, p.Order.UserID, p.BalanceToDeduct); err != nil {
			slog.Error("[CRITICAL] rollback failed", "orderID", p.OrderID, "amount", p.BalanceToDeduct, "error", err)
			s.writeAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED", "admin", map[string]any{"gatewayError": psErrMsg(gErr), "rollbackError": psErrMsg(err), "balanceDeducted": p.BalanceToDeduct})
			return false
		}
		s.invalidateRefundBalanceCache(ctx, p.Order.UserID)
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if _, err := s.subscriptionSvc.ExtendSubscription(ctx, p.SubscriptionID, p.SubDaysToDeduct); err != nil {
			slog.Error("[CRITICAL] subscription rollback failed", "orderID", p.OrderID, "subID", p.SubscriptionID, "days", p.SubDaysToDeduct, "error", err)
			s.writeAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED", "admin", map[string]any{"gatewayError": psErrMsg(gErr), "rollbackError": psErrMsg(err), "subDaysDeducted": p.SubDaysToDeduct})
			return false
		}
	}
	return true
}

func (s *PaymentService) restoreStatus(ctx context.Context, p *RefundPlan) {
	rs := OrderStatusCompleted
	if p.Order.Status == OrderStatusRefundRequested {
		rs = OrderStatusRefundRequested
	}
	_, _ = s.entClient.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(rs).Save(ctx)
}
