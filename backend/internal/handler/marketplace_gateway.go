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
}

func (h *OpenAIGatewayHandler) SetMarketplaceGateway(g *service.MarketplaceGateway) {
	h.marketplaceGateway = g
}

// marketplaceRequest returns nil for ordinary groups and fails closed when a
// shared group cannot be priced or is disabled.
func (h *OpenAIGatewayHandler) marketplaceRequest(c *gin.Context, key *service.APIKey, body []byte, model, protocol string) (*marketplaceRequestState, []byte, bool) {
	gateway := h.marketplaceGateway
	if gateway == nil || gateway.Catalog == nil || key == nil || key.GroupID == nil {
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
	digest := service.HashUsageRequestPayload(append([]byte(protocol+"\x00"), body...))
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
		h.errorResponse(c, http.StatusConflict, "operation_in_progress", "Shared request already exists")
		return false
	}
	s.receipt = receipt
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
		return nil, err
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
	attempt, err := s.gateway.Store.Dispatch(ctx, s.receipt.ID, q)
	if err != nil {
		return nil, err
	}
	if attempt == nil {
		return nil, service.ErrMarketplaceRequestState
	}
	s.quote = q
	s.attempt = attempt
	return prepared, nil
}
func (s *marketplaceRequestState) finish(c *gin.Context, result *service.OpenAIForwardResult, forwardErr error) {
	original := s.writer.ResponseWriter
	defer func() { c.Writer = original }()
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
	_ = s.writer.Finish(captured)
	if !captured && !original.Written() && c.Request.Context().Err() == nil {
		c.Writer = original
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": "Shared usage settlement unavailable"}})
	}
	_ = forwardErr
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
