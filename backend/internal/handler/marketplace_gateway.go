package handler

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type marketplaceRequestState struct {
	gateway           *service.MarketplaceGateway
	receipt           *service.MarketplaceReceipt
	attempt           *service.MarketplaceAttempt
	quote             service.QuoteSnapshot
	budget            service.Money
	outputLimit       int64
	buyer, key, group int64
	digest            string
	idempotency       *string
	writer            *marketplaceResponseWriter
	dispatchErr       error
	clientContext     context.Context
}

func (h *OpenAIGatewayHandler) SetMarketplaceGateway(g *service.MarketplaceGateway) {
	h.marketplaceGateway = g
}

const marketplaceRawDigestKey = "marketplace_raw_request_digest"

func marketplaceRawDigest(protocol string, body []byte) string {
	return service.HashUsageRequestPayload(append([]byte(protocol+"\x00"), body...))
}

// lookupMarketplaceReplay is read-only. When admission set replay-only, every
// miss or lookup error terminates the request before any new dispatch.
func (h *OpenAIGatewayHandler) lookupMarketplaceReplay(c *gin.Context, key *service.APIKey, digest string, required bool) bool {
	raw := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if h.marketplaceGateway == nil || key == nil || key.GroupID == nil || raw == "" {
		if required {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Shared request lookup unavailable")
			return true
		}
		return false
	}
	lookup, ok := h.marketplaceGateway.Store.(interface {
		LookupReplay(context.Context, service.MarketplacePrepareCommand) (*service.MarketplaceReceipt, error)
	})
	if !ok {
		if required {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Shared request lookup unavailable")
			return true
		}
		return false
	}
	command := service.MarketplacePrepareCommand{BuyerUserID: key.UserID, APIKeyID: key.ID, GroupID: *key.GroupID, RequestDigest: digest, IdempotencyKey: &raw}
	receipt, err := lookup.LookupReplay(c.Request.Context(), command)
	if err != nil {
		status, code := http.StatusServiceUnavailable, "api_error"
		if errors.Is(err, service.ErrMarketplaceIdempotencyConflict) {
			status, code = http.StatusConflict, "idempotency_conflict"
		}
		h.errorResponse(c, status, code, "Shared request lookup failed")
		return true
	}
	if receipt != nil {
		writeMarketplaceReplay(c, receipt)
		return true
	}
	if required {
		h.errorResponse(c, http.StatusConflict, "idempotency_conflict", "Shared request replay does not match")
		return true
	}
	return false
}

// marketplaceRequest returns nil for ordinary groups and fails closed when a
// shared group cannot be priced or is disabled.
func (h *OpenAIGatewayHandler) marketplaceRequest(c *gin.Context, key *service.APIKey, body []byte, model, protocol string) (*marketplaceRequestState, []byte, bool) {
	gateway := h.marketplaceGateway
	replayOnly := middleware2.IsMarketplaceReplayOnly(c)
	if gateway == nil || key == nil || key.GroupID == nil {
		if replayOnly {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Shared request lookup unavailable")
			return nil, nil, false
		}
		return nil, body, true
	}
	if !replayOnly {
		if gateway.Catalog == nil {
			return nil, body, true
		}
		shared, err := gateway.Catalog.IsSharedGroup(c.Request.Context(), *key.GroupID)
		if err != nil {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Marketplace catalog unavailable")
			return nil, nil, false
		}
		if !shared {
			return nil, body, true
		}
	}
	digest := marketplaceRawDigest(protocol, body)
	if v, ok := c.Get(marketplaceRawDigestKey); ok {
		if raw, ok := v.(string); ok && raw != "" {
			digest = raw
		}
	}
	if h.lookupMarketplaceReplay(c, key, digest, replayOnly) {
		return nil, nil, false
	}
	if h.cfg == nil || !h.cfg.Marketplace.Enabled || !h.cfg.Marketplace.DispatchEnabled || config.NormalizeRunMode(h.cfg.RunMode) != config.RunModeStandard || gateway.Store == nil || gateway.Runtime == nil || key.Group == nil || key.Group.Platform != service.PlatformOpenAI {
		h.errorResponse(c, http.StatusServiceUnavailable, "marketplace_disabled", "Marketplace dispatch is unavailable")
		return nil, nil, false
	}
	if protocol == "responses" && !isBareOpenAIResponsesPath(c) {
		h.errorResponse(c, http.StatusBadRequest, "unsupported_shared_protocol", "Unsupported shared endpoint")
		return nil, nil, false
	}
	quote, contextLimit, outputLimit, err := gateway.Catalog.QuoteTemplate(c.Request.Context(), *key.GroupID, model, protocol)
	if err != nil || quote.RateMultiplier == nil || quote.PublicModel != model || quote.Protocol != protocol || contextLimit <= 0 || outputLimit <= 0 || outputLimit > contextLimit {
		h.errorResponse(c, http.StatusBadRequest, "price_unavailable", "Shared model price is unavailable")
		return nil, nil, false
	}
	providerBound, attested := h.cfg.Marketplace.AttestedInputCeiling(key.Group.Platform, quote.ActualModel)
	if !attested || contextLimit < providerBound {
		h.errorResponse(c, http.StatusServiceUnavailable, "price_unavailable", "Shared provider input limit is not verified")
		return nil, nil, false
	}
	capped, err := service.MarketplaceEnforceTextRequest(body, protocol, outputLimit)
	if err != nil || gjson.GetBytes(capped, "model").String() != model {
		h.errorResponse(c, http.StatusBadRequest, "unsupported_shared_protocol", "Unsupported shared request or output limit")
		return nil, nil, false
	}
	now := time.Now()
	rate := h.gatewayService.ResolveUserGroupRateMultiplier(c.Request.Context(), key.UserID, *key.GroupID, key.Group.RateMultiplier) * key.Group.PeakMultiplierAt(now)
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		h.errorResponse(c, http.StatusBadRequest, "price_unavailable", "Shared model price is unavailable")
		return nil, nil, false
	}
	published := *quote.RateMultiplier
	consumer := decimal.NewFromFloat(rate)
	effective := published.Mul(consumer)
	quote.PublishedRateMultiplier = &published
	quote.ConsumerRateMultiplier = &consumer
	quote.RateMultiplier = &effective
	quote.SnapshotAt = now.UTC()
	// Dummy IDs are used only by the pure budget calculation, never persisted.
	budgetQuote := quote
	budgetQuote.AccountID = 1
	budgetQuote.OfferID = 1
	budgetQuote.OwnerUserID = 1
	budget, err := service.NewMarketplaceQuoteService().ComputeUpperBound(budgetQuote, contextLimit, outputLimit)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "price_unavailable", "Shared model price is unavailable")
		return nil, nil, false
	}
	var idempotency *string
	if raw := strings.TrimSpace(c.GetHeader("Idempotency-Key")); raw != "" {
		idempotency = &raw
	}
	state := &marketplaceRequestState{gateway: gateway, quote: quote, budget: budget, outputLimit: outputLimit, buyer: key.UserID, key: key.ID, group: *key.GroupID, digest: digest, idempotency: idempotency}
	if strings.TrimSpace(c.GetHeader("x-codex-turn-state")) != "" {
		h.errorResponse(c, http.StatusBadRequest, "unsupported_shared_protocol", "Unsupported shared session reference")
		return nil, nil, false
	}
	c.Request.Header = c.Request.Header.Clone()
	for _, name := range []string{"session_id", "conversation_id", "x-session-id"} {
		if value := c.Request.Header.Get(name); value != "" {
			c.Request.Header.Set(name, state.scopeSession(value))
		}
	}
	// Never let a user-controlled upstream cache key join contexts across buyers.
	if cache := gjson.GetBytes(capped, "prompt_cache_key"); cache.Exists() && cache.Type == gjson.String {
		scoped := service.HashUsageRequestPayload([]byte(strings.Join([]string{cache.String(), strconv.FormatInt(key.UserID, 10), strconv.FormatInt(key.ID, 10)}, "\x00")))
		capped, err = sjson.SetBytes(capped, "prompt_cache_key", scoped)
		if err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Invalid shared request")
			return nil, nil, false
		}
	}
	return state, capped, true
}
func writeMarketplaceReplay(c *gin.Context, receipt *service.MarketplaceReceipt) {
	status, code := http.StatusConflict, "operation_in_progress"
	switch receipt.Status {
	case "settled", "voided":
		status, code = http.StatusOK, "operation_completed"
	case "needs_review", "usage_pending":
		code = "operation_needs_review"
	}
	c.JSON(status, gin.H{"receipt_id": receipt.ID, "status": receipt.Status, "code": code})
}

func (s *marketplaceRequestState) reserve(c *gin.Context, h *OpenAIGatewayHandler) bool {
	receipt, created, err := s.gateway.Store.PrepareAndReserve(c.Request.Context(), service.MarketplacePrepareCommand{BuyerUserID: s.buyer, APIKeyID: s.key, GroupID: s.group, RequestDigest: s.digest, IdempotencyKey: s.idempotency}, s.budget)
	if err != nil {
		status := http.StatusServiceUnavailable
		code := "api_error"
		if errors.Is(err, service.ErrMarketplaceIdempotencyConflict) {
			status = http.StatusConflict
			code = "idempotency_conflict"
		}
		h.errorResponse(c, status, code, "Shared request could not be reserved")
		return false
	}
	if !created || receipt == nil {
		if receipt == nil {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Shared request unavailable")
			return false
		}
		writeMarketplaceReplay(c, receipt)
		return false
	}
	s.receipt = receipt
	s.clientContext = c.Request.Context()
	c.Request = c.Request.WithContext(service.WithMarketplaceOutputLimit(service.WithMarketplaceExpectedModel(service.WithMarketplaceBuyer(c.Request.Context(), s.buyer), s.quote.ActualModel), s.outputLimit))
	return true
}
func (s *marketplaceRequestState) abortUndispatched() {
	if s == nil || s.receipt == nil || s.attempt != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.gateway.Store.VoidUndispatched(ctx, s.receipt.ID)
}
func (s *marketplaceRequestState) dispatch(ctx context.Context, a *service.Account) (*service.Account, error) {
	if a == nil || a.OwnerUserID == nil || *a.OwnerUserID == s.buyer {
		return nil, service.ErrMarketplaceAccountIneligible
	}
	offer, err := s.gateway.Store.Qualify(ctx, a.ID, s.buyer, s.group, s.quote.ActualModel)
	if err != nil {
		return nil, err
	}
	if offer <= 0 {
		return nil, service.ErrMarketplaceAccountIneligible
	}
	prepared, err := s.gateway.Runtime.Prepare(ctx, a)
	if err != nil {
		// No attempt has been dispatched yet, so another supplier is safe to try.
		return nil, errors.Join(service.ErrMarketplaceAccountIneligible, err)
	}
	// Mapping is request-local and makes the published actual model authoritative
	// while retaining the public name for the existing response adapters.
	prepared.Credentials["model_mapping"] = map[string]any{s.quote.PublicModel: s.quote.ActualModel}
	prepared.Extra = make(map[string]any, len(a.Extra))
	for k, v := range a.Extra {
		prepared.Extra[k] = v
	}
	prepared.Extra["openai_passthrough"] = false
	prepared.Extra["openai_oauth_passthrough"] = false
	prepared.Extra["openai_ws_force_http"] = true
	q := s.quote
	q.AccountID = a.ID
	q.OwnerUserID = *a.OwnerUserID
	q.OfferID = offer
	multiplier := a.BillingRateMultiplier()
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier < 0 {
		return nil, service.ErrMarketplaceAccountIneligible
	}
	rate := decimal.NewFromFloat(multiplier)
	q.AccountRateMultiplier = &rate
	s.quote = q
	return prepared, nil
}

func (s *marketplaceRequestState) commitDispatch(ctx context.Context) error {
	if s.clientContext != nil && s.clientContext.Err() != nil {
		return s.clientContext.Err()
	}
	if s.attempt != nil {
		return service.ErrMarketplaceRequestState
	}
	attempt, err := s.gateway.Store.Dispatch(ctx, s.receipt.ID, s.quote)
	if err != nil {
		s.dispatchErr = err
		return err
	}
	if attempt == nil {
		s.dispatchErr = service.ErrMarketplaceRequestState
		return s.dispatchErr
	}
	s.attempt = attempt
	return nil
}

// Bound downstream writes as well as upstream work. Without this, a connected
// client that stops reading can keep the account slot occupied beyond its TTL.
func marketplaceBoundClientWrites(c *gin.Context, execution context.Context) (func(), error) {
	deadline, ok := execution.Deadline()
	if !ok {
		return nil, errors.New("shared execution deadline unavailable")
	}
	controller := http.NewResponseController(c.Writer)
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	return func() { _ = controller.SetWriteDeadline(time.Time{}) }, nil
}

func (s *marketplaceRequestState) clientConnected(c *gin.Context) bool {
	if s.clientContext != nil {
		return s.clientContext.Err() == nil
	}
	return c.Request.Context().Err() == nil
}
func (s *marketplaceRequestState) respondTimeout(c *gin.Context, h *OpenAIGatewayHandler, streamStarted bool) {
	if s != nil && c.Request.Context().Err() != nil && s.clientConnected(c) {
		h.handleStreamingAwareError(c, http.StatusGatewayTimeout, "api_error", "Shared execution timed out", streamStarted)
	}
}
func (s *marketplaceRequestState) finish(c *gin.Context, result *service.OpenAIForwardResult, forwardErr error) {
	original := s.writer.ResponseWriter
	defer func() { c.Writer = original }()
	if s.attempt == nil {
		if s.writer.status >= 400 && s.writer.Written() && !s.writer.failed {
			_ = s.writer.Finish(true)
		} else if !original.Written() && s.clientConnected(c) {
			c.Writer = original
			status := http.StatusBadRequest
			if s.dispatchErr != nil {
				status = http.StatusServiceUnavailable
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "api_error", "message": "Shared request could not be dispatched"}})
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
	defer cancel()
	captured := false
	if result != nil && result.UsageReported && result.Usage.ImageInputTokens == 0 && result.Usage.ImageOutputTokens == 0 && result.Usage.ImageCacheReadTokens == 0 {
		u := result.Usage
		usage := service.UsageSnapshot{InputTokens: int64(u.InputTokens), OutputTokens: int64(u.OutputTokens), CacheReadTokens: int64(u.CacheReadInputTokens), CacheWriteTokens: int64(u.CacheCreationInputTokens), Source: "upstream"}
		cmd := service.UsageBillingCommand{RequestID: s.receipt.ID, APIKeyID: s.key, UserID: s.buyer, AccountID: s.quote.AccountID, Model: s.quote.PublicModel, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens, CacheCreationTokens: u.CacheCreationInputTokens}
		settled, err := s.gateway.Store.Complete(ctx, s.receipt.ID, s.attempt.ID, s.quote, usage, cmd)
		captured = err == nil && settled != nil && !settled.NeedsReview
	}
	if !captured {
		_ = s.gateway.Store.MarkReview(ctx, s.receipt.ID, s.attempt.ID, "usage_or_capture_unavailable")
	}
	deliveryErr := s.writer.Finish(captured)
	if deliveryErr != nil && s.clientConnected(c) {
		c.Writer = original
		if !original.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": "Shared response delivery failed", "receipt_id": s.receipt.ID}})
		} else if s.writer.streaming {
			_, _ = original.WriteString("event: error\ndata: {\"error\":{\"type\":\"api_error\",\"message\":\"Shared response delivery failed\",\"receipt_id\":\"" + s.receipt.ID + "\"}}\n\n")
			original.Flush()
		}
		return
	}
	if captured && forwardErr != nil && !s.writer.terminal && s.clientConnected(c) {
		c.Writer = original
		if !original.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": "Shared upstream request failed", "receipt_id": s.receipt.ID}})
		} else if s.writer.streaming {
			_, _ = original.WriteString("event: error\ndata: {\"error\":{\"type\":\"upstream_error\",\"message\":\"Shared upstream request failed\",\"receipt_id\":\"" + s.receipt.ID + "\"}}\n\n")
			original.Flush()
		}
		return
	}
	if !captured && s.clientConnected(c) {
		c.Writer = original
		if !original.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": "Shared usage settlement unavailable", "receipt_id": s.receipt.ID}})
		} else if s.writer.streaming {
			_, _ = original.WriteString("event: error\ndata: {\"error\":{\"type\":\"api_error\",\"message\":\"Shared usage settlement unavailable\",\"receipt_id\":\"" + s.receipt.ID + "\"}}\n\n")
			original.Flush()
		}
	}
}
func (s *marketplaceRequestState) scopeSession(value string) string {
	return service.HashUsageRequestPayload([]byte(strings.Join([]string{"marketplace", strconv.FormatInt(s.buyer, 10), strconv.FormatInt(s.key, 10), value}, "\x00")))
}

func marketplaceSupportedRoute(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	switch strings.TrimRight(path, "/") {
	case "/v1/responses", "/responses", "/backend-api/codex/responses", "/openai/v1/responses", "/v1/chat/completions", "/chat/completions", "/backend-api/codex/chat/completions", "/openai/v1/chat/completions":
		return true
	default:
		return false
	}
}

// MarketplaceRouteGuard is attached after API-key authentication on gateway
// routes so unsupported modalities cannot fall through to ordinary billing.
func (h *OpenAIGatewayHandler) MarketplaceRouteGuard(c *gin.Context) {
	if middleware2.IsMarketplaceReplayOnly(c) {
		c.Next()
		return
	}
	if h == nil || h.marketplaceGateway == nil || h.marketplaceGateway.Catalog == nil {
		c.Next()
		return
	}
	key, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || key.GroupID == nil {
		c.Next()
		return
	}
	shared, err := h.marketplaceGateway.Catalog.IsSharedGroup(c.Request.Context(), *key.GroupID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Marketplace catalog unavailable"}})
		return
	}
	if shared && !marketplaceSupportedRoute(c.Request.Method, c.Request.URL.Path) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "unsupported_shared_protocol", "message": "Unsupported shared endpoint"}})
		return
	}
	c.Next()
}
