package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const freeRetrySuccessSSE = "data: {\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n"

func freeRetryFixture(t *testing.T, serve func(http.ResponseWriter, *http.Request, int)) (*OpenAIGatewayService, *freeGatewayRepo, *gin.Context, *httptest.ResponseRecorder, func() []int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var mu sync.Mutex
	var accounts []int64
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env freeWorkerEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		accounts = append(accounts, env.AccountID)
		attempt := len(accounts)
		mu.Unlock()
		serve(w, r, attempt)
	}))
	t.Cleanup(worker.Close)
	repo := &freeGatewayRepo{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{ID: 4})
	return svc, repo, c, w, func() []int64 {
		mu.Lock()
		defer mu.Unlock()
		return append([]int64(nil), accounts...)
	}
}

func freeRetryAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile, "pool_mode": true, "pool_mode_retry_count": 10}}
}

func freeRetryFault(w http.ResponseWriter, kind string) {
	if kind == "worker" {
		w.Header().Set("X-Sub2api-Transport-Result", "worker-error")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"error":{"code":"upstream_error","message":"upstream_error"}}`))
		return
	}
	w.Header().Set("X-Sub2api-Transport-Result", "upstream")
	w.Header().Set("X-Sub2api-Dispatched", "true")
	switch kind {
	case "server":
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable"}}`))
	case "ordinary429":
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"try later"}}`))
	case "quota":
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"FreeUsageLimitError","message":"Free usage limit reached"}}`))
	case "empty":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(openCodeFreeEmptyAnswerFixture()))
	case "truncated":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	}
}

func TestOpenCodeFreeRuntimeFailureRetriesOriginalOnce(t *testing.T) {
	for _, kind := range []string{"worker", "server", "ordinary429", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			svc, repo, c, w, accounts := freeRetryFixture(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if n == 1 {
					freeRetryFault(w, kind)
					return
				}
				w.Header().Set("X-Sub2api-Transport-Result", "upstream")
				w.Header().Set("X-Sub2api-Dispatched", "true")
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(freeRetrySuccessSSE))
			})
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, freeRetryAccount(1), []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`), "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, []int64{1, 1}, accounts(), "the retry must use the original account, regardless of its pool retry count")
			require.Equal(t, 1, repo.unknown)
			require.Contains(t, w.Body.String(), `"content":"ok"`)
			require.NotContains(t, w.Body.String(), "temporarily unavailable")
		})
	}
}

func TestOpenCodeFreeRuntimeBudgetAllowsOneFinalReplacement(t *testing.T) {
	for _, finalKind := range []string{"worker", "server", "quota", "empty", "truncated"} {
		t.Run(finalKind, func(t *testing.T) {
			svc, repo, c, _, accounts := freeRetryFixture(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if n <= 2 {
					freeRetryFault(w, "server")
					return
				}
				freeRetryFault(w, finalKind)
			})
			body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`)
			_, err := svc.ForwardAsChatCompletions(context.Background(), c, freeRetryAccount(1), body, "", "")
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.True(t, failover.ShouldRetryNextAccount())
			require.False(t, failover.RetryableOnSameAccount, "same-account retry is internal and already exhausted")
			require.False(t, c.Writer.Written())
			_, err = svc.ForwardAsChatCompletions(context.Background(), c, freeRetryAccount(2), body, "", "")
			require.Error(t, err)
			failover = nil
			if errors.As(err, &failover) {
				require.False(t, failover.ShouldRetryNextAccount(), "the final replacement may not retry or switch again")
			}
			require.Equal(t, []int64{1, 1, 2}, accounts())
			require.Equal(t, 3, repo.unknown)
		})
	}
}

func TestOpenCodeFreeCommittedRuntimeFailureIsNotReplayed(t *testing.T) {
	svc, repo, c, w, accounts := freeRetryFixture(t, func(w http.ResponseWriter, r *http.Request, _ int) { freeRetryFault(w, "worker") })
	c.Header("Content-Type", "text/event-stream")
	_, _ = c.Writer.WriteString(": keepalive\n\n")
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, freeRetryAccount(1), []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`), "", "")
	require.Error(t, err)
	require.Equal(t, []int64{1}, accounts())
	require.Equal(t, 1, repo.unknown)
	require.Contains(t, w.Body.String(), `data: {"error"`)
}

func TestOpenCodeFreeVisiblePartialAnswerIsNotReplayed(t *testing.T) {
	svc, repo, c, w, accounts := freeRetryFixture(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	})
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, freeRetryAccount(1), []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`), "", "")
	require.Error(t, err)
	require.Equal(t, []int64{1}, accounts())
	require.Equal(t, 1, repo.unknown)
	require.Contains(t, w.Body.String(), "partial")
	require.Contains(t, w.Body.String(), `data: {"error"`)
	require.NotContains(t, w.Body.String(), "[DONE]")
}

func TestOpenCodeFreeCanceledRequestDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc, repo, c, _, accounts := freeRetryFixture(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		cancel()
		freeRetryFault(w, "server")
	})
	_, err := svc.ForwardAsChatCompletions(ctx, c, freeRetryAccount(1), []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`), "", "")
	require.Error(t, err)
	require.Equal(t, []int64{1}, accounts())
	require.Equal(t, 1, repo.unknown)
	require.False(t, c.Writer.Written())
}

func TestOpenCodeFreeHeldStreamFailureEndsWithJSONError(t *testing.T) {
	svc, _, c, w, accounts := freeRetryFixture(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			freeRetryFault(w, "truncated")
			return
		}
		freeRetryFault(w, "server")
	})
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, freeRetryAccount(1), body, "", "")
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Empty(t, c.Writer.Header().Get("Content-Type"), "an uncommitted failed SSE attempt must not poison the handler's final error MIME")
	_, err = svc.ForwardAsChatCompletions(context.Background(), c, freeRetryAccount(2), body, "", "")
	require.Error(t, err)
	require.Equal(t, []int64{1, 1, 2}, accounts())
	require.Equal(t, 503, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "application/json")
	require.JSONEq(t, `{"error":{"message":"temporarily unavailable"}}`, w.Body.String())
}
