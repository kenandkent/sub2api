//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This repository replaces durable accounting only; request normalization,
// worker HTTP dispatch, SSE validation, account selection and handler failover
// all run through their production implementations.
type freeBunHandlerUsageRepo struct {
	service.UsageBillingRepository
	mu       sync.Mutex
	attempts []int64
	unknown  int
}

func (r *freeBunHandlerUsageRepo) BeginOpenCodeFreeAttempt(_ context.Context, _ *service.OpenCodeFreeUsageMeta, _, accountID int64, _, _ string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, accountID)
	return nil
}

func (r *freeBunHandlerUsageRepo) MarkOpenCodeFreeUnknown(context.Context, *service.OpenCodeFreeUsageMeta, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unknown++
	return nil
}

func (r *freeBunHandlerUsageRepo) ResolveOpenCodeFreeSession(_ context.Context, _, candidate string, _ time.Duration) (string, error) {
	return candidate, nil
}

func (*freeBunHandlerUsageRepo) StageOpenCodeFreeUsage(context.Context, *service.OpenCodeFreeUsageMeta, *service.UsageBillingCommand, *service.UsageLog, *service.OpenCodeFreeBillingContext) error {
	return nil
}

func (*freeBunHandlerUsageRepo) SettleOpenCodeFreeUsage(context.Context, string) (*service.OpenCodeFreeSettlement, error) {
	return nil, nil
}

func (*freeBunHandlerUsageRepo) RecoverOpenCodeFreeUsage(context.Context) ([]service.OpenCodeFreeRecoveredUsage, error) {
	return nil, nil
}

func (r *freeBunHandlerUsageRepo) snapshot() ([]int64, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.attempts...), r.unknown
}

type freeBunHandlerWorker struct {
	mu       sync.Mutex
	hits     []int64
	answers  []string
	protocol []string
}

func (w *freeBunHandlerWorker) ServeHTTP(out http.ResponseWriter, req *http.Request) {
	var envelope struct {
		AccountID int64           `json:"account_id"`
		Body      json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(req.Body).Decode(&envelope); err != nil {
		http.Error(out, "bad envelope", http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	w.hits = append(w.hits, envelope.AccountID)
	answer := "success"
	if len(w.answers) > 0 {
		answer, w.answers = w.answers[0], w.answers[1:]
	}
	if req.URL.Path != "/v1/transport/chat" || req.Header.Get("Authorization") != "Bearer 12345678901234567890123456789012" || gjson.GetBytes(envelope.Body, "model").String() != "big-pickle" {
		w.protocol = append(w.protocol, "invalid worker request")
	}
	w.mu.Unlock()
	out.Header().Set("X-Sub2api-Transport-Result", "upstream")
	out.Header().Set("X-Sub2api-Dispatched", "true")
	switch answer {
	case "runtime":
		out.Header().Set("Content-Type", "application/json")
		out.WriteHeader(http.StatusServiceUnavailable)
		_, _ = out.Write([]byte(`{"error":{"code":"overloaded","message":"temporarily unavailable"}}`))
	case "quota":
		out.Header().Set("Content-Type", "application/json")
		out.Header().Set("Retry-After", "74005")
		out.WriteHeader(http.StatusTooManyRequests)
		_, _ = out.Write([]byte(`{"error":{"code":"FreeUsageLimitError","message":"Free usage limit reached"}}`))
	case "malformed":
		out.Header().Set("Content-Type", "text/event-stream")
		_, _ = out.Write([]byte("data: not-json\n\n"))
	default:
		content := "hello"
		if answer == "empty" {
			content = ""
		}
		out.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(out, "data: {\"id\":\"answer\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n", content)
	}
}

func (w *freeBunHandlerWorker) snapshot() ([]int64, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int64(nil), w.hits...), append([]string(nil), w.protocol...)
}

func newFreeBunRetryHandler(t *testing.T, answers ...string) (*OpenAIGatewayHandler, *freeBunHandlerWorker, *freeBunHandlerUsageRepo) {
	t.Helper()
	worker := &freeBunHandlerWorker{answers: append([]string(nil), answers...)}
	server := httptest.NewServer(worker)
	t.Cleanup(server.Close)
	accounts := make([]service.Account, 0, 3)
	for i := int64(1); i <= 3; i++ {
		accounts = append(accounts, service.Account{
			ID: i, Name: fmt.Sprintf("free-%d", i), Platform: service.PlatformOpenCodeGo,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(i),
			Credentials: map[string]any{"account_mode": "zen", "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": "opencode-big-pickle-text-balanced-20261027-v2"},
		})
	}
	accountRepo := &grokCredentialHandlerRepo{accounts: accounts}
	usageRepo := &freeBunHandlerUsageRepo{}
	cfg := &config.Config{RunMode: config.RunModeSimple, OpenCodeFreeBun: config.OpenCodeFreeBunConfig{
		Enabled: true, WorkerEndpoint: server.URL, Token: "12345678901234567890123456789012",
		RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16,
	}}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewOpenAIGatewayService(accountRepo, nil, usageRepo, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), service.NewRateLimitService(accountRepo, nil, cfg, nil, nil), billing,
		nil, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = 10 // The free transport budget must override the general pool budget.
	return h, worker, usageRepo
}

func freeBunRetryContext(t *testing.T, stream bool) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	groupID := int64(991)
	body := fmt.Sprintf(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 992, GroupID: &groupID, User: &service.User{ID: 993, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenCodeGo, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 993, Concurrency: 0})
	return c, rec
}

func TestChatCompletionsFreeBunRetryBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		answers     []string
		wantHits    []int64
		wantStatus  int
		wantUnknown int
	}{
		{"transient original account recovers", []string{"runtime", "success"}, []int64{1, 1}, http.StatusOK, 1},
		{"original retry then final account succeeds", []string{"runtime", "runtime", "success"}, []int64{1, 1, 2}, http.StatusOK, 2},
		{"final runtime failure stops before third account", []string{"runtime", "runtime", "runtime", "success"}, []int64{1, 1, 2}, http.StatusServiceUnavailable, 3},
		{"final quota failure stops before third account", []string{"runtime", "runtime", "quota", "success"}, []int64{1, 1, 2}, http.StatusTooManyRequests, 3},
		{"final empty failure stops before third account", []string{"runtime", "runtime", "empty", "success"}, []int64{1, 1, 2}, http.StatusBadGateway, 3},
		{"final parser failure stops before third account", []string{"runtime", "runtime", "malformed", "success"}, []int64{1, 1, 2}, http.StatusBadGateway, 3},
		{"quota on original retry still makes replacement final", []string{"runtime", "quota", "runtime", "success"}, []int64{1, 1, 2}, http.StatusServiceUnavailable, 3},
		{"empty on original retry still makes replacement final", []string{"runtime", "empty", "quota", "success"}, []int64{1, 1, 2}, http.StatusTooManyRequests, 3},
		{"quota immediately switches account", []string{"quota", "success"}, []int64{1, 2}, http.StatusOK, 1},
		{"empty immediately switches account", []string{"empty", "success"}, []int64{1, 2}, http.StatusOK, 1},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				h, worker, usage := newFreeBunRetryHandler(t, tc.answers...)
				c, rec := freeBunRetryContext(t, stream)
				h.ChatCompletions(c)
				hits, protocolErrors := worker.snapshot()
				require.Empty(t, protocolErrors)
				require.Equal(t, tc.wantHits, hits, "must enforce exact account retry order; response: %s", rec.Body.String())
				attempts, unknown := usage.snapshot()
				require.Equal(t, tc.wantHits, attempts, "every dispatched attempt needs its own accounting lease")
				require.Equal(t, tc.wantUnknown, unknown, "every failed attempt must be marked unknown")
				require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
				if tc.wantStatus == http.StatusOK {
					require.Contains(t, rec.Body.String(), "hello")
					require.NotContains(t, rec.Body.String(), "temporarily unavailable")
					if stream {
						require.Contains(t, rec.Body.String(), "[DONE]")
					}
				} else {
					require.NotContains(t, rec.Body.String(), "hello", "a forbidden later attempt must never replace the final failure")
				}
			})
		}
	}
}

func TestChatCompletionsFreeBunRetryBudgetDoesNotReplayCommittedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, worker, usage := newFreeBunRetryHandler(t, "runtime", "success")
	c, rec := freeBunRetryContext(t, true)
	c.Header("Content-Type", "text/event-stream")
	_, err := c.Writer.WriteString(": keepalive\n\n")
	require.NoError(t, err)
	c.Writer.Flush()

	h.ChatCompletions(c)

	hits, protocolErrors := worker.snapshot()
	require.Empty(t, protocolErrors)
	require.Equal(t, []int64{1}, hits, "a committed response must prevent both retry and failover")
	attempts, unknown := usage.snapshot()
	require.Equal(t, []int64{1}, attempts)
	require.Equal(t, 1, unknown)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), ": keepalive")
	require.Contains(t, rec.Body.String(), "temporarily unavailable")
	require.NotContains(t, rec.Body.String(), "hello")
}
