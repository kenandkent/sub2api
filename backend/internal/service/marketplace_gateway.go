package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MarketplaceGatewayCatalog exposes published, unbound quotes to the gateway.
type MarketplaceGatewayCatalog interface {
	IsSharedGroup(context.Context, int64) (bool, error)
	QuoteTemplate(context.Context, int64, string, string) (QuoteSnapshot, int64, int64, error)
}
type MarketplaceGatewayStore interface {
	PrepareAndReserve(context.Context, MarketplacePrepareCommand, Money) (*MarketplaceReceipt, bool, error)
	Qualify(context.Context, int64, int64, int64, string) (int64, error)
	Dispatch(context.Context, string, QuoteSnapshot) (*MarketplaceAttempt, error)
	Complete(context.Context, string, int64, QuoteSnapshot, UsageSnapshot, UsageBillingCommand) (*MarketplaceBillingResult, error)
	MarkReview(context.Context, string, int64, string) error
	VoidUndispatched(context.Context, string) error
}
type MarketplaceGateway struct {
	Catalog MarketplaceGatewayCatalog
	Store   MarketplaceGatewayStore
	Runtime *SupplierRuntimeAccountService
}

func NewMarketplaceGateway(c MarketplaceGatewayCatalog, s MarketplaceGatewayStore, r *SupplierRuntimeAccountService) *MarketplaceGateway {
	return &MarketplaceGateway{Catalog: c, Store: s, Runtime: r}
}

var ErrMarketplaceUnsupportedRequest = errors.New("unsupported shared request")

type marketplaceBuyerContextKey struct{}
type marketplaceModelContextKey struct{}
type marketplaceOutputLimitContextKey struct{}
type marketplaceExecutionContextKey struct{}
type marketplaceBeforeSendContextKey struct{}

// WithMarketplaceBeforeSend commits the attempt immediately before transport.
func WithMarketplaceBeforeSend(ctx context.Context, fn func(context.Context) error) context.Context {
	return context.WithValue(ctx, marketplaceBeforeSendContextKey{}, fn)
}

func marketplaceBeforeSend(ctx context.Context) error {
	if fn, ok := ctx.Value(marketplaceBeforeSendContextKey{}).(func(context.Context) error); ok {
		return fn(ctx)
	}
	if isMarketplaceRequest(ctx) {
		return ErrMarketplaceRequestState
	}
	return nil
}

// Bound the detached execution, including header wait and draining after disconnect.
const marketplaceExecutionTimeout = 5 * time.Minute

func WithMarketplaceExecution(ctx context.Context, timeoutSeconds int) (context.Context, context.CancelFunc) {
	timeout := marketplaceExecutionTimeout
	if timeoutSeconds > 0 && timeoutSeconds <= 3600 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return marketplaceWithExecutionTimeout(ctx, timeout)
}

func marketplaceWithExecutionTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	execution, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	return context.WithValue(ctx, marketplaceExecutionContextKey{}, execution), cancel
}

func marketplaceExecutionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	execution, _ := ctx.Value(marketplaceExecutionContextKey{}).(context.Context)
	return execution
}

// Supplier slots follow upstream execution rather than client connection lifetime.
func MarketplaceSlotReleaseContext(ctx context.Context) context.Context {
	if isMarketplaceRequest(ctx) {
		return context.WithoutCancel(ctx)
	}
	return ctx
}

// WithMarketplaceBuyer is used only after a request has been authenticated and
// identified as a shared-group request. Fresh qualification is still mandatory.
func WithMarketplaceBuyer(ctx context.Context, buyer int64) context.Context {
	return context.WithValue(ctx, marketplaceBuyerContextKey{}, buyer)
}
func WithMarketplaceExpectedModel(ctx context.Context, actual string) context.Context {
	return context.WithValue(ctx, marketplaceModelContextKey{}, actual)
}
func WithMarketplaceOutputLimit(ctx context.Context, limit int64) context.Context {
	return context.WithValue(ctx, marketplaceOutputLimitContextKey{}, limit)
}

// Validate the final transformed payload immediately before constructing an upstream request.
func marketplaceValidateOutbound(ctx context.Context, body []byte) error {
	if !isMarketplaceRequest(ctx) {
		return nil
	}
	var obj map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&obj) != nil || obj == nil {
		return ErrMarketplaceUnsupportedRequest
	}
	if hasMarketplaceUnsupportedValue(obj) {
		return ErrMarketplaceUnsupportedRequest
	}
	if prompt, ok := obj["prompt"]; ok {
		if _, valid := prompt.(string); !valid {
			return ErrMarketplaceUnsupportedRequest
		}
	}
	if tools, ok := obj["tools"]; ok {
		list, valid := tools.([]any)
		if !valid {
			return ErrMarketplaceUnsupportedRequest
		}
		for _, raw := range list {
			tool, valid := raw.(map[string]any)
			if !valid || tool["type"] != "function" {
				return ErrMarketplaceUnsupportedRequest
			}
		}
	}
	model, ok := obj["model"].(string)
	if !ok || !marketplaceModelMatches(ctx, model) {
		return ErrMarketplacePriceUnavailable
	}
	limit, _ := ctx.Value(marketplaceOutputLimitContextKey{}).(int64)
	if limit <= 0 {
		return ErrMarketplaceUnsupportedRequest
	}
	found := false
	for _, field := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if raw, ok := obj[field]; ok {
			num, ok := raw.(json.Number)
			if !ok {
				return ErrMarketplaceUnsupportedRequest
			}
			value, err := num.Int64()
			if err != nil || value <= 0 || value > limit {
				return ErrMarketplaceUnsupportedRequest
			}
			found = true
		}
	}
	if !found {
		return ErrMarketplaceUnsupportedRequest
	}
	return nil
}
func marketplaceModelMatches(ctx context.Context, actual string) bool {
	if !isMarketplaceRequest(ctx) {
		return true
	}
	expected, _ := ctx.Value(marketplaceModelContextKey{}).(string)
	return expected != "" && expected == actual
}
func isMarketplaceRequest(ctx context.Context) bool {
	buyer, _ := ctx.Value(marketplaceBuyerContextKey{}).(int64)
	return buyer > 0
}
func marketplaceAccountMatchesBuyer(ctx context.Context, a *Account) bool {
	buyer, _ := ctx.Value(marketplaceBuyerContextKey{}).(int64)
	if buyer <= 0 {
		return a.OwnerUserID == nil
	}
	return a.OwnerUserID != nil && *a.OwnerUserID > 0 && *a.OwnerUserID != buyer && a.ParentAccountID == nil
}

// MarketplaceEnforceTextRequest sets an actual upstream output cap, and rejects
// modalities or server-side tools whose costs are not covered by the quote.
func MarketplaceEnforceTextRequest(body []byte, protocol string, maxOutput int64) ([]byte, error) {
	if maxOutput <= 0 || (protocol != "responses" && protocol != "chat_completions") {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	var obj map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&obj) != nil || obj == nil {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	if !marketplaceAllowedTopLevelFields(obj, protocol) {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	if prompt, ok := obj["prompt"]; ok {
		if _, stringPrompt := prompt.(string); !stringPrompt {
			return nil, ErrMarketplaceUnsupportedRequest
		}
	}
	if hasMarketplaceUnsupportedValue(obj) {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	// An ID in a Responses input item can refer to server-side state. We do
	// not have a durable ownership and size binding for such references.
	if protocol == "responses" && marketplaceInputHasID(obj["input"]) {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	if background, ok := obj["background"]; ok && background != false {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	for _, field := range []string{"conversation", "client_metadata", "context_management", "previous_response_id"} {
		if _, ok := obj[field]; ok {
			return nil, ErrMarketplaceUnsupportedRequest
		}
	}
	if tier, ok := obj["service_tier"]; ok && tier != "default" {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	if protocol == "chat_completions" {
		if _, ok := obj["input"]; ok {
			return nil, ErrMarketplaceUnsupportedRequest
		}
		if _, ok := obj["max_output_tokens"]; ok {
			return nil, ErrMarketplaceUnsupportedRequest
		}
	} else {
		if _, ok := obj["max_tokens"]; ok {
			return nil, ErrMarketplaceUnsupportedRequest
		}
		if _, ok := obj["max_completion_tokens"]; ok {
			return nil, ErrMarketplaceUnsupportedRequest
		}
	}
	if tools, ok := obj["tools"]; ok {
		list, ok := tools.([]any)
		if !ok {
			return nil, ErrMarketplaceUnsupportedRequest
		}
		for _, raw := range list {
			tool, ok := raw.(map[string]any)
			if !ok || tool["type"] != "function" {
				return nil, ErrMarketplaceUnsupportedRequest
			}
		}
	}
	if n, ok := obj["n"]; ok && fmt.Sprint(n) != "1" {
		return nil, ErrMarketplaceUnsupportedRequest
	}
	fields := []string{"max_output_tokens"}
	if protocol == "chat_completions" {
		fields = []string{"max_tokens", "max_completion_tokens"}
	}
	found := false
	for _, field := range fields {
		if raw, ok := obj[field]; ok {
			num, ok := raw.(json.Number)
			if !ok {
				return nil, ErrMarketplaceUnsupportedRequest
			}
			value, err := num.Int64()
			if err != nil || value <= 0 || value > maxOutput {
				return nil, ErrMarketplaceUnsupportedRequest
			}
			found = true
		}
	}
	if !found {
		if protocol == "responses" {
			obj["max_output_tokens"] = maxOutput
		} else {
			obj["max_completion_tokens"] = maxOutput
		}
	}
	if protocol == "chat_completions" && obj["stream"] == true {
		obj["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(obj)
}
func hasMarketplaceUnsupportedValue(v any) bool {
	switch value := v.(type) {
	case map[string]any:
		if raw, ok := value["type"].(string); ok {
			t := strings.ToLower(raw)
			if strings.Contains(t, "image") || strings.Contains(t, "audio") || strings.Contains(t, "video") || strings.Contains(t, "search") || t == "computer_use_preview" || t == "file_search" || t == "input_file" || t == "item_reference" || t == "compaction" || t == "additional_tools" || t == "mcp" || t == "code_interpreter" || t == "computer" || t == "shell" {
				return true
			}
		}
		for key, child := range value {
			k := strings.ToLower(key)
			if k == "modalities" || k == "audio" || k == "image_url" || k == "web_search_options" || k == "encrypted_content" || k == "file_id" || k == "file_url" || k == "file_data" || k == "previous_response_id" {
				return true
			}
			if hasMarketplaceUnsupportedValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if hasMarketplaceUnsupportedValue(child) {
				return true
			}
		}
	}
	return false
}

func marketplaceInputHasID(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v["id"]; ok {
			return true
		}
		for _, child := range v {
			if marketplaceInputHasID(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if marketplaceInputHasID(child) {
				return true
			}
		}
	}
	return false
}

// Unknown top-level fields can activate provider-side state or unpriced tools.
// Shared requests are limited to the inline text/function compatibility surface.
func marketplaceAllowedTopLevelFields(obj map[string]any, protocol string) bool {
	common := map[string]bool{"model": true, "background": true, "stream": true, "stream_options": true, "temperature": true, "top_p": true, "user": true, "service_tier": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true, "prompt_cache_key": true, "prompt_cache_retention": true, "safety_identifier": true, "store": true, "metadata": true, "seed": true, "frequency_penalty": true, "presence_penalty": true, "stop": true, "reasoning_effort": true}
	responses := map[string]bool{"input": true, "instructions": true, "prompt": true, "messages": true, "commands": true, "reasoning": true, "text": true, "max_output_tokens": true, "max_tool_calls": true, "include": true, "truncation": true, "top_logprobs": true}
	chat := map[string]bool{"messages": true, "instructions": true, "max_tokens": true, "max_completion_tokens": true, "n": true, "functions": true, "function_call": true, "response_format": true, "logprobs": true, "top_logprobs": true, "logit_bias": true, "prompt_cache_options": true}
	for k := range obj {
		if common[k] {
			continue
		}
		if protocol == "responses" && responses[k] {
			continue
		}
		if protocol == "chat_completions" && chat[k] {
			continue
		}
		return false
	}
	return true
}
