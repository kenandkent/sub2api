//go:build unit

package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type marketplaceHTTPStore struct {
	calls, captures, reviews, voids         int
	reserves                                int
	duplicate, dispatchError, completeError bool
	beforeComplete                          func()
	usage                                   service.UsageSnapshot
}

func (s *marketplaceHTTPStore) PrepareAndReserve(_ context.Context, c service.MarketplacePrepareCommand, _ service.Money) (*service.MarketplaceReceipt, bool, error) {
	s.reserves++
	return &service.MarketplaceReceipt{ID: "receipt", BuyerUserID: c.BuyerUserID}, !s.duplicate, nil
}
func (s *marketplaceHTTPStore) Qualify(context.Context, int64, int64, int64, string) (int64, error) {
	return 50, nil
}
func (s *marketplaceHTTPStore) Dispatch(_ context.Context, _ string, q service.QuoteSnapshot) (*service.MarketplaceAttempt, error) {
	s.calls++
	if s.dispatchError {
		return nil, context.DeadlineExceeded
	}
	return &service.MarketplaceAttempt{ID: 40, Quote: q}, nil
}
func (s *marketplaceHTTPStore) Complete(_ context.Context, _ string, _ int64, _ service.QuoteSnapshot, usage service.UsageSnapshot, _ service.UsageBillingCommand) (*service.MarketplaceBillingResult, error) {
	s.captures++
	s.usage = usage
	if s.completeError {
		return nil, errors.New("capture unavailable")
	}
	if s.beforeComplete != nil {
		s.beforeComplete()
	}
	return &service.MarketplaceBillingResult{FirstApplied: true}, nil
}
func (s *marketplaceHTTPStore) MarkReview(context.Context, string, int64, string) error {
	s.reviews++
	return nil
}
func (s *marketplaceHTTPStore) VoidUndispatched(context.Context, string) error { s.voids++; return nil }

type marketplaceHTTPCatalog struct{}

func (marketplaceHTTPCatalog) IsSharedGroup(context.Context, int64) (bool, error) { return true, nil }
func (marketplaceHTTPCatalog) QuoteTemplate(_ context.Context, _ int64, model, protocol string) (service.QuoteSnapshot, int64, int64, error) {
	rate := decimal.NewFromInt(1)
	price := service.Money{Decimal: decimal.NewFromInt(1)}
	return service.QuoteSnapshot{Currency: "USD", PublicModel: model, ActualModel: "gpt-5.4", Protocol: protocol, PriceVersion: 1, PolicyVersion: 1, RateMultiplier: &rate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &price, OutputPerMillion: &price, CacheReadPerMillion: &price, CacheWritePerMillion: &price}}, SnapshotAt: time.Now().UTC()}, 1000, 200, nil
}

type marketplaceHTTPRuntimeStore struct{ envelope service.EncryptedCredential }

func (s marketplaceHTTPRuntimeStore) LoadSupplierCredential(context.Context, int64, int64) (service.EncryptedCredential, error) {
	return s.envelope, nil
}
func (s marketplaceHTTPRuntimeStore) CompareAndSwapSupplierCredential(context.Context, int64, int64, service.EncryptedCredential, service.EncryptedCredential) (bool, error) {
	return false, nil
}

type marketplaceHTTPUpstream struct {
	service.HTTPUpstream
	calls       int
	status      int
	body        string
	contentType string
	requestBody []byte
}

func (u *marketplaceHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	u.requestBody, _ = io.ReadAll(req.Body)
	contentType := u.contentType
	if contentType == "" {
		contentType = "application/json"
	}
	return &http.Response{StatusCode: u.status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}
func newMarketplaceHTTPTest(t *testing.T, up *marketplaceHTTPUpstream, store service.MarketplaceGatewayStore, groupHooks ...func(*service.Group)) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	env, err := service.NewCredentialEnvelope("key", map[string][]byte{"key": bytes.Repeat([]byte{3}, 32)})
	require.NoError(t, err)
	raw, _ := json.Marshal(map[string]any{"api_key": "secret"})
	sealed, err := env.Seal("openai", 11, 13, raw)
	require.NoError(t, err)
	owner, group := int64(11), int64(12)
	account := service.Account{ID: 13, OwnerUserID: &owner, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group}, Credentials: map[string]any{"_supplier_envelope": sealed}, Extra: map[string]any{"openai_responses_supported": true}}
	repo := &grokCredentialHandlerRepo{accounts: []service.Account{account}, missingOnGet: map[int64]bool{}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.ConcurrencySlotTTLMinutes = 15
	cfg.Marketplace.Enabled = true
	cfg.Marketplace.DispatchEnabled = true
	cfg.Marketplace.Providers = []config.MarketplaceProviderConfig{{Platform: "openai", Models: []string{"gpt-5.4"}, MaxBillableInputTokensByModel: map[string]int64{"gpt-5.4": 1000}}}
	cache := newHandlerInflightCache(100)
	billing := service.NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	slots := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
	concurrency := service.NewConcurrencyService(slots)
	gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billing, up, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	runtime := service.NewSupplierRuntimeAccountService(env, marketplaceHTTPRuntimeStore{sealed}, nil, nil, service.SupplierRuntimeProviderConfig{})
	h.SetMarketplaceGateway(service.NewMarketplaceGateway(marketplaceHTTPCatalog{}, store, runtime))
	key := &service.APIKey{ID: 14, UserID: 15, GroupID: &group, User: &service.User{ID: 15, Status: service.StatusActive}, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1}}
	for _, hook := range groupHooks {
		hook(key.Group)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Writer = &marketplaceDeadlineTestWriter{ResponseWriter: c.Writer}
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 15, Concurrency: 1})
		if c.GetHeader("X-Test-Replay-Only") == "true" {
			c.Set("marketplace_replay_only", true)
		}
		c.Next()
	})
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/chat/completions", h.ChatCompletions)
	return router
}
func TestMarketplaceGatewayHTTPDoesNotRetryUnknown429(t *testing.T) {
	upstream := &marketplaceHTTPUpstream{status: 429, body: `{"error":{"message":"rate limited"}}`}
	store := &marketplaceHTTPStore{}
	router := newMarketplaceHTTPTest(t, upstream, store)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hello"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, 1, upstream.calls, "body: %s", rec.Body.String())
	require.Equal(t, 1, store.calls)
	require.Equal(t, 1, store.reviews)
	require.Zero(t, store.captures)
	require.Zero(t, store.voids)
	require.Equal(t, int64(200), gjson.GetBytes(upstream.requestBody, "max_output_tokens").Int())
}
func TestMarketplaceGatewayHTTPDuplicateNeverDispatches(t *testing.T) {
	upstream := &marketplaceHTTPUpstream{status: 429, body: `{}`}
	store := &marketplaceHTTPStore{duplicate: true}
	router := newMarketplaceHTTPTest(t, upstream, store)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hello"}`))
	req.Header.Set("Idempotency-Key", "same")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Zero(t, upstream.calls)
	require.Zero(t, store.calls)
}
func TestMarketplaceGatewayHTTPReportedUsageIsCaptured(t *testing.T) {
	upstream := &marketplaceHTTPUpstream{status: 200, body: `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}`}
	store := &marketplaceHTTPStore{}
	router := newMarketplaceHTTPTest(t, upstream, store)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hello"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, 1, upstream.calls, "body: %s", rec.Body.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, store.captures)
	require.Zero(t, store.reviews)
	require.Equal(t, "public", gjson.Get(rec.Body.String(), "model").String())
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.requestBody, "model").String())
}
func TestMarketplaceGatewayHTTPSSEWaitsForCapture(t *testing.T) {
	upstream := &marketplaceHTTPUpstream{status: 200, contentType: "text/event-stream", body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":3}}}}\n\n"}
	started := make(chan struct{})
	release := make(chan struct{})
	store := &marketplaceHTTPStore{beforeComplete: func() { close(started); <-release }}
	router := newMarketplaceHTTPTest(t, upstream, store)
	server := httptest.NewServer(router)
	defer server.Close()
	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"public","input":"hi","stream":true}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, line, "response.output_text.delta")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not begin")
	}
	finished := make(chan string, 1)
	go func() { rest, _ := io.ReadAll(reader); finished <- string(rest) }()
	select {
	case rest := <-finished:
		t.Fatalf("stream completed before settlement: %q", rest)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case rest := <-finished:
		require.Contains(t, rest, "response.completed")
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not finish")
	}
	require.Equal(t, int64(10), store.usage.InputTokens)
	require.Equal(t, int64(3), store.usage.CacheReadTokens)
}

func TestMarketplaceGatewayHTTPFailClosedRequests(t *testing.T) {
	for _, body := range []string{
		`{"model":"public","input":"hi","background":true}`,
		`{"model":"public","input":"hi","tools":[{"type":"code_interpreter"}]}`,
		`{"model":"public","input":"hi","tools":[{"type":"mcp","server_label":"x"}]}`,
		`{"model":"public","input":"hi","service_tier":"priority"}`,
		`{"model":"public","input":"hi","max_output_tokens":201}`,
		`{"model":"public","model":"other","input":"hi"}`,
	} {
		t.Run(body, func(t *testing.T) {
			upstream := &marketplaceHTTPUpstream{status: 200, body: `{}`}
			store := &marketplaceHTTPStore{}
			router := newMarketplaceHTTPTest(t, upstream, store)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, upstream.calls)
			require.Zero(t, store.calls)
		})
	}
}

func TestMarketplaceGatewayHTTPUsagePresence(t *testing.T) {
	for _, tc := range []struct {
		name, usage     string
		capture, review int
		status          int
	}{
		{"zero", ` ,"usage":{"input_tokens":0,"output_tokens":0}`, 1, 0, http.StatusOK},
		{"missing", "", 0, 1, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &marketplaceHTTPUpstream{status: 200, body: `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[]` + tc.usage + `}`}
			store := &marketplaceHTTPStore{}
			router := newMarketplaceHTTPTest(t, upstream, store)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hi"}`)))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, tc.capture, store.captures)
			require.Equal(t, tc.review, store.reviews)
		})
	}
}

func TestMarketplaceGatewayHTTPChatFunctionTool(t *testing.T) {
	upstream := &marketplaceHTTPUpstream{status: 200, contentType: "text/event-stream", body: `data: {"type":"response.completed","response":{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":10,"output_tokens":4}}}` + "\n\n"}
	store := &marketplaceHTTPStore{}
	router := newMarketplaceHTTPTest(t, upstream, store)
	body := `{"model":"public","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, store.captures)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.requestBody, "model").String())
	require.Equal(t, int64(200), gjson.GetBytes(upstream.requestBody, "max_output_tokens").Int())
	require.Equal(t, "lookup", gjson.Get(rec.Body.String(), "choices.0.message.tool_calls.0.function.name").String())
}

func TestMarketplaceGatewayHTTPPartialStreamWithoutUsageNeedsReview(t *testing.T) {
	upstream := &marketplaceHTTPUpstream{status: 200, contentType: "text/event-stream", body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"}
	store := &marketplaceHTTPStore{}
	router := newMarketplaceHTTPTest(t, upstream, store)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hi","stream":true}`)))
	require.Contains(t, rec.Body.String(), "partial")
	require.Zero(t, store.captures)
	require.Equal(t, 1, store.reviews)
	require.NotContains(t, rec.Body.String(), "response.completed")
}

func TestMarketplaceGatewayHTTPChatDoesNotRetryStructuredInputError(t *testing.T) {
	upstream := &marketplaceHTTPUpstream{status: 400, body: `{"error":{"type":"invalid_request_error","code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`}
	store := &marketplaceHTTPStore{}
	router := newMarketplaceHTTPTest(t, upstream, store)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"public","messages":[{"role":"user","content":"hi"}]}`)))
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, 1, store.reviews)
	require.Zero(t, store.captures)
}

func TestMarketplaceFailedTerminalUsageSettlesButPartialUsageDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name, body        string
		captures, reviews int
	}{
		{"terminal failed without preceding output", "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fail\",\"status\":\"failed\",\"error\":{\"message\":\"failed\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n", 1, 0},
		{"incomplete terminal usage", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fail\",\"status\":\"failed\",\"error\":{\"message\":\"failed\"},\"usage\":{\"input_tokens\":10}}}\n\n", 0, 1},
		{"terminal failed", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_fail\",\"status\":\"failed\",\"error\":{\"message\":\"failed\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n", 1, 0},
		{"nonterminal usage then eof", "data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &marketplaceHTTPUpstream{status: 200, contentType: "text/event-stream", body: tc.body}
			store := &marketplaceHTTPStore{}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hello","stream":true}`))
			newMarketplaceHTTPTest(t, up, store).ServeHTTP(rec, req)
			require.Equal(t, 1, up.calls)
			require.Equal(t, tc.captures, store.captures)
			require.Equal(t, tc.reviews, store.reviews)
			if tc.captures == 1 {
				require.EqualValues(t, 10, store.usage.InputTokens)
				require.EqualValues(t, 4, store.usage.OutputTokens)
				require.Contains(t, rec.Body.String(), "failed")
			}
		})
	}
}

type marketplaceDigestMemoryStore struct {
	marketplaceHTTPStore
	digest  string
	receipt *service.MarketplaceReceipt
}

func (s *marketplaceDigestMemoryStore) LookupReplay(_ context.Context, c service.MarketplacePrepareCommand) (*service.MarketplaceReceipt, error) {
	if s.receipt == nil {
		return nil, nil
	}
	if c.RequestDigest != s.digest {
		return nil, service.ErrMarketplaceIdempotencyConflict
	}
	return s.receipt, nil
}
func (s *marketplaceDigestMemoryStore) PrepareAndReserve(_ context.Context, c service.MarketplacePrepareCommand, _ service.Money) (*service.MarketplaceReceipt, bool, error) {
	s.digest = c.RequestDigest
	s.receipt = &service.MarketplaceReceipt{ID: "receipt-raw", Status: "budget_reserved"}
	return s.receipt, true, nil
}
func (s *marketplaceDigestMemoryStore) Complete(ctx context.Context, receipt string, attempt int64, q service.QuoteSnapshot, u service.UsageSnapshot, b service.UsageBillingCommand) (*service.MarketplaceBillingResult, error) {
	result, err := s.marketplaceHTTPStore.Complete(ctx, receipt, attempt, q, u, b)
	if err == nil {
		s.receipt.Status = "settled"
	}
	return result, err
}
func TestMarketplaceRawDigestReplayBeforeMutableReasoningPolicy(t *testing.T) {
	up := &marketplaceHTTPUpstream{status: 200, body: `{"id":"resp","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4}}`}
	store := &marketplaceDigestMemoryStore{}
	var group *service.Group
	router := newMarketplaceHTTPTest(t, up, store, func(g *service.Group) { group = g; g.MaxReasoningEffort = "medium" })
	send := func(body string, replay bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "same")
		if replay {
			req.Header.Set("X-Test-Replay-Only", "true")
		}
		router.ServeHTTP(rec, req)
		return rec
	}
	high := `{"model":"public","input":"hello","reasoning":{"effort":"high"}}`
	medium := `{"model":"public","input":"hello","reasoning":{"effort":"medium"}}`
	first := send(high, false)
	require.Equal(t, 200, first.Code, first.Body.String())
	require.Equal(t, 1, up.calls)
	require.Equal(t, 409, send(medium, true).Code, "different raw requests must conflict")
	group.MaxReasoningEffort = "low"
	group.MaxReasoningEffortOverLimit = service.ReasoningEffortOverLimitDeny
	replay := send(high, true)
	require.Equal(t, 200, replay.Code, replay.Body.String())
	require.Contains(t, replay.Body.String(), `"status":"settled"`)
	require.Equal(t, 1, up.calls)
}

func TestMarketplaceRawDigestDistinguishesBOMBeforeNormalization(t *testing.T) {
	up := &marketplaceHTTPUpstream{status: 200, body: `{"id":"resp","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4}}`}
	store := &marketplaceDigestMemoryStore{}
	router := newMarketplaceHTTPTest(t, up, store)
	send := func(body string, replay bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "same")
		if replay {
			req.Header.Set("X-Test-Replay-Only", "true")
		}
		router.ServeHTTP(w, req)
		return w
	}
	body := `{"model":"public","input":"hello"}`
	first := send("\ufeff"+body, false)
	require.Equal(t, 200, first.Code, first.Body.String())
	retry := send(body, true)
	require.Equal(t, 409, retry.Code, retry.Body.String())
	require.Equal(t, 1, up.calls)
}

func TestMarketplaceStoredPromptRejectedBeforeReservation(t *testing.T) {
	up := &marketplaceHTTPUpstream{status: 200, body: `{}`}
	store := &marketplaceHTTPStore{}
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","prompt":{"id":"pmpt_supplier_template"}}`))
	rec := httptest.NewRecorder()
	newMarketplaceHTTPTest(t, up, store).ServeHTTP(rec, req)
	require.Equal(t, 400, rec.Code, rec.Body.String())
	require.Zero(t, store.reserves)
	require.Zero(t, up.calls)
}

func TestMarketplaceChatRawDigestReplayBeforeReasoningPolicy(t *testing.T) {
	up := &marketplaceHTTPUpstream{status: 200, contentType: "text/event-stream", body: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n"}
	store := &marketplaceDigestMemoryStore{}
	var group *service.Group
	router := newMarketplaceHTTPTest(t, up, store, func(g *service.Group) { group = g; g.MaxReasoningEffort = "medium" })
	send := func(body string, replay bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "same")
		if replay {
			req.Header.Set("X-Test-Replay-Only", "true")
		}
		router.ServeHTTP(w, req)
		return w
	}
	high := `{"model":"public","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`
	medium := `{"model":"public","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"medium"}`
	first := send(high, false)
	require.Equal(t, 200, first.Code, first.Body.String())
	require.Equal(t, 1, up.calls)
	require.Equal(t, 409, send(medium, true).Code)
	group.MaxReasoningEffort = "low"
	group.MaxReasoningEffortOverLimit = service.ReasoningEffortOverLimitDeny
	replay := send(high, true)
	require.Equal(t, 200, replay.Code, replay.Body.String())
	require.Equal(t, 1, up.calls)
}

func TestMarketplaceStreamingSettlementFailureEmitsTerminalError(t *testing.T) {
	for _, tc := range []struct {
		name, terminal string
		captureError   bool
	}{
		{"missing usage", `{"type":"response.completed","response":{"id":"resp","status":"completed","output":[]}}`, false},
		{"capture unavailable", `{"type":"response.completed","response":{"id":"resp","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &marketplaceHTTPUpstream{status: 200, contentType: "text/event-stream", body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: " + tc.terminal + "\n\n"}
			store := &marketplaceHTTPStore{completeError: tc.captureError}
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"hi","stream":true}`))
			newMarketplaceHTTPTest(t, up, store).ServeHTTP(w, req)
			require.Contains(t, w.Body.String(), "partial")
			require.Contains(t, w.Body.String(), "event: error")
			require.Contains(t, w.Body.String(), "receipt")
			require.NotContains(t, w.Body.String(), "response.completed")
			require.Equal(t, 1, store.reviews)
		})
	}
}
