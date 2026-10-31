//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const adminFreeGoodStream = "data: {\"id\":\"answer\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n"

func adminFreeAccount() *Account {
	return &Account{ID: 401, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile,
		"model_mapping": map[string]any{"my-alias": "big-pickle"},
	}}
}

func adminFreeService(account *Account, endpoint string) (*AccountTestService, *httpUpstreamRecorder) {
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse())
	svc.cfg = &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: endpoint, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 2, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}
	return svc, upstream
}

func TestAdminOpenCodeFreeUsesWorkerWithMappedProfileAndProxy(t *testing.T) {
	received := make(chan freeWorkerEnvelope, 1)
	auth := make(chan string, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env freeWorkerEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		received <- env
		auth <- r.Header.Get("Authorization")
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(adminFreeGoodStream))
	}))
	defer worker.Close()
	account := adminFreeAccount()
	proxyID := int64(9)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: 9, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "user", Password: "secret"}
	svc, upstream := adminFreeService(account, worker.URL)
	c, recorder := newTestContext()
	err := svc.TestAccountConnection(c, account.ID, "my-alias", "check prompt", AccountTestModeDefault)
	require.NoError(t, err)
	select {
	case env := <-received:
		require.Equal(t, "Bearer 12345678901234567890123456789012", <-auth)
		require.Equal(t, "opencode-big-pickle-text-balanced-20261027-v2", env.ProfileID)
		require.Equal(t, "da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9", env.ProfileSHA256)
		require.Equal(t, int64(401), env.AccountID)
		require.Equal(t, "big-pickle", gjson.GetBytes(env.Body, "model").String())
		require.Equal(t, "check prompt", gjson.GetBytes(env.Body, "messages.0.content").String())
		require.NotNil(t, env.Proxy)
		require.Equal(t, "http://user:secret@proxy.example:8080", env.Proxy.URL)
		require.Equal(t, int64(9), env.Proxy.ID)
		require.NotEmpty(t, env.Proxy.Generation)
		require.NotEmpty(t, env.RequestID)
		require.NotEmpty(t, env.SessionID)
	default:
		t.Fatal("Bun worker did not receive the admin test")
	}
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), `"type":"test_start","model":"big-pickle"`)
	require.Contains(t, recorder.Body.String(), `"type":"content","text":"hello"`)
	require.Contains(t, recorder.Body.String(), `"type":"test_complete","success":true`)
	require.NotContains(t, recorder.Body.String(), "secret")
}

func TestAdminOpenCodeFreeRejectsInvalidWorkerResponses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		headers     bool
		contentType string
		body        string
	}{
		{"incomplete", 200, true, "text/event-stream", strings.TrimSuffix(adminFreeGoodStream, "data: [DONE]\n\n")},
		{"paid", 200, true, "text/event-stream", strings.Replace(adminFreeGoodStream, `"billing_source":"free"`, `"billing_source":"paid"`, 1)},
		{"missing billing source", 200, true, "text/event-stream", strings.Replace(adminFreeGoodStream, `,"billing_source":"free"`, "", 1)},
		{"wrong content type", 200, true, "application/json", adminFreeGoodStream},
		{"malformed", 200, true, "text/event-stream", "data: not-json\n\ndata: [DONE]\n\n"},
		{"worker failure", 502, false, "application/json", `{"error":"secret"}`},
		{"upstream non-200", 429, true, "application/json", `{"error":"secret"}`},
		{"upstream unauthorized", 401, true, "application/json", `{"error":"secret"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.headers {
					w.Header().Set("X-Sub2api-Transport-Result", "upstream")
					w.Header().Set("X-Sub2api-Dispatched", "true")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer worker.Close()
			account := adminFreeAccount()
			svc, upstream := adminFreeService(account, worker.URL)
			c, recorder := newTestContext()
			err := svc.TestAccountConnection(c, account.ID, "big-pickle", "hi", AccountTestModeDefault)
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			repo := svc.accountRepo.(*openAIAccountTestRepo)
			require.Zero(t, repo.setErrorID)
			require.Zero(t, repo.rateLimitedID)
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
			require.NotContains(t, recorder.Body.String(), `"type":"test_complete"`)
			require.NotContains(t, recorder.Body.String(), "secret")
			require.Contains(t, recorder.Body.String(), "request_id=ocfree_")
			require.Contains(t, recorder.Body.String(), "profile=opencode-big-pickle")
			if tc.status != 200 {
				require.Contains(t, recorder.Body.String(), fmt.Sprintf("HTTP %d", tc.status))
			}
		})
	}
}

func TestAdminOpenCodeFreeDisabledDoesNotFallBack(t *testing.T) {
	account := adminFreeAccount()
	svc, upstream := adminFreeService(account, "http://127.0.0.1:1")
	svc.cfg.OpenCodeFreeBun.Enabled = false
	c, recorder := newTestContext()
	err := svc.TestAccountConnection(c, account.ID, "big-pickle", "hi", AccountTestModeDefault)
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
	require.NotContains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestAdminOpenCodeFreeFailsClosedForBadConfigurationAndProxy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*AccountTestService, *Account)
	}{
		{"missing config", func(s *AccountTestService, _ *Account) { s.cfg = nil }},
		{"invalid worker URL", func(s *AccountTestService, _ *Account) {
			s.cfg.OpenCodeFreeBun.WorkerEndpoint = "http://worker.example"
		}},
		{"unreachable worker", func(s *AccountTestService, _ *Account) { s.cfg.OpenCodeFreeBun.WorkerEndpoint = "http://127.0.0.1:1" }},
		{"missing bound proxy", func(_ *AccountTestService, a *Account) { id := int64(9); a.ProxyID = &id }},
		{"non-http proxy", func(_ *AccountTestService, a *Account) {
			id := int64(9)
			a.ProxyID = &id
			a.Proxy = &Proxy{ID: 9, Protocol: "socks5", Host: "host", Port: 1080, Username: "private", Password: "secret"}
		}},
		{"mismatched proxy", func(_ *AccountTestService, a *Account) {
			id := int64(9)
			a.ProxyID = &id
			a.Proxy = &Proxy{ID: 10, Protocol: "http", Host: "host", Port: 8080}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := adminFreeAccount()
			svc, upstream := adminFreeService(account, "http://127.0.0.1:1")
			tc.configure(svc, account)
			c, recorder := newTestContext()
			err := svc.TestAccountConnection(c, account.ID, "big-pickle", "hi", AccountTestModeDefault)
			require.Error(t, err)
			require.Empty(t, upstream.requests)
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
			require.NotContains(t, recorder.Body.String(), `"type":"test_complete"`)
			require.NotContains(t, recorder.Body.String(), "secret")
			require.NotContains(t, recorder.Body.String(), "private")
			require.NotContains(t, recorder.Body.String(), "12345678901234567890123456789012")
		})
	}
}

func TestAdminOpenCodeFreeDefaultsModelAndPromptWithoutAccounting(t *testing.T) {
	received := make(chan freeWorkerEnvelope, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env freeWorkerEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		received <- env
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(adminFreeGoodStream))
	}))
	defer worker.Close()
	account := adminFreeAccount()
	svc, upstream := adminFreeService(account, worker.URL)
	repo := &freeGatewayRepo{}
	svc.openaiGatewayService = &OpenAIGatewayService{usageBillingRepo: repo, cfg: svc.cfg}
	c, recorder := newTestContext()
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault))
	env := <-received
	require.Equal(t, "big-pickle", gjson.GetBytes(env.Body, "model").String())
	require.Equal(t, "hi", gjson.GetBytes(env.Body, "messages.0.content").String())
	require.Contains(t, recorder.Body.String(), `"type":"test_start","model":"big-pickle"`)
	require.Empty(t, repo.profileHash)
	require.Zero(t, repo.unknown)
	require.Empty(t, repo.sessions)
	require.Empty(t, upstream.requests)
}

func TestAdminOpenCodeFreeCancellationStopsWorker(t *testing.T) {
	started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-release:
		}
	}))
	defer func() { close(release); worker.Close() }()
	account := adminFreeAccount()
	svc, upstream := adminFreeService(account, worker.URL)
	c, recorder := newTestContext()
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	result := make(chan error, 1)
	go func() { result <- svc.TestAccountConnection(c, account.ID, "big-pickle", "hi", AccountTestModeDefault) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not receive request")
	}
	cancel()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("test did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not observe cancellation")
	}
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
	require.NotContains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestAdminOpenCodeFreeDefaultsToAccountMappedModel(t *testing.T) {
	received := make(chan freeWorkerEnvelope, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env freeWorkerEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		received <- env
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.ReplaceAll(adminFreeGoodStream, "big-pickle", "deepseek-v4.1-flash-free")))
	}))
	defer worker.Close()
	account := adminFreeAccount()
	account.Credentials["model_mapping"] = map[string]any{"deepseek-v4.1-flash-free": "deepseek-v4.1-flash-free"}
	svc, _ := adminFreeService(account, worker.URL)
	c, recorder := newTestContext()
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault))
	env := <-received
	require.Equal(t, "deepseek-v4.1-flash-free", gjson.GetBytes(env.Body, "model").String())
	require.Equal(t, "opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1", env.ProfileID)
	require.Contains(t, recorder.Body.String(), `"type":"test_start","model":"deepseek-v4.1-flash-free"`)
}

func TestAdminOpenCodeFreeRedactsCredentialsFromContent(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Replace(adminFreeGoodStream, "hello", "hello 12345678901234567890123456789012 proxy-user proxy-secret", 1)))
	}))
	defer worker.Close()
	account := adminFreeAccount()
	id := int64(9)
	account.ProxyID = &id
	account.Proxy = &Proxy{ID: id, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "proxy-user", Password: "proxy-secret"}
	svc, _ := adminFreeService(account, worker.URL)
	c, recorder := newTestContext()
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "big-pickle", "hi", AccountTestModeDefault))
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
	require.NotContains(t, recorder.Body.String(), "12345678901234567890123456789012")
	require.NotContains(t, recorder.Body.String(), "proxy-user")
	require.NotContains(t, recorder.Body.String(), "proxy-secret")
}

func TestAdminOpenCodeFreeDefaultsToSupportedWildcardMappedTarget(t *testing.T) {
	received := make(chan freeWorkerEnvelope, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env freeWorkerEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		received <- env
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.ReplaceAll(adminFreeGoodStream, "big-pickle", "deepseek-v4.1-flash-free")))
	}))
	defer worker.Close()
	account := adminFreeAccount()
	account.Credentials["model_mapping"] = map[string]any{"deepseek-*": "deepseek-v4.1-flash-free"}
	svc, _ := adminFreeService(account, worker.URL)
	c, recorder := newTestContext()
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault))
	env := <-received
	require.Equal(t, "deepseek-v4.1-flash-free", gjson.GetBytes(env.Body, "model").String())
	require.Equal(t, "opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1", env.ProfileID)
	require.Contains(t, recorder.Body.String(), `"type":"test_start","model":"deepseek-v4.1-flash-free"`)
}

func TestAdminOpenCodeFreeDefaultsToCrossFamilyWildcard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping map[string]any
	}{
		{"cross family", map[string]any{"claude-*": "deepseek-v4.1-flash-free"}},
		{"exact override", map[string]any{"claude-*": "deepseek-v4.1-flash-free", "claude-": "paid-model"}},
		{"both representatives overridden", map[string]any{"claude-*": "deepseek-v4.1-flash-free", "claude-": "paid-model", "claude-opencode-free-test": "paid-model"}},
		{"longer wildcard override", map[string]any{"claude-*": "deepseek-v4.1-flash-free", "claude-": "paid-model", "claude-opencode-free-test": "paid-model", "claude-a*": "paid-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan freeWorkerEnvelope, 1)
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var env freeWorkerEnvelope
				if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				received <- env
				w.Header().Set("X-Sub2api-Transport-Result", "upstream")
				w.Header().Set("X-Sub2api-Dispatched", "true")
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(strings.ReplaceAll(adminFreeGoodStream, "big-pickle", "deepseek-v4.1-flash-free")))
			}))
			defer worker.Close()
			account := adminFreeAccount()
			account.Credentials["model_mapping"] = tc.mapping
			svc, upstream := adminFreeService(account, worker.URL)
			c, recorder := newTestContext()
			require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault))
			env := <-received
			require.Equal(t, "deepseek-v4.1-flash-free", gjson.GetBytes(env.Body, "model").String())
			require.Equal(t, "opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1", env.ProfileID)
			require.Contains(t, recorder.Body.String(), `"type":"test_start","model":"deepseek-v4.1-flash-free"`)
			require.Empty(t, upstream.requests)
		})
	}
}

// A quota-exhausted instance found by the admin diagnostic is parked just like
// in the gateway, while the upstream body stays private to the admin stream.
func TestAdminOpenCodeFreeQuotaExhaustionParksInstance(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Retry-After", "74005")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"FreeUsageLimitError","message":"Free usage limit reached"}}`))
	}))
	defer worker.Close()
	account := adminFreeAccount()
	svc, upstream := adminFreeService(account, worker.URL)
	c, recorder := newTestContext()
	err := svc.TestAccountConnection(c, account.ID, "big-pickle", "hi", AccountTestModeDefault)
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), "HTTP 429")
	require.NotContains(t, recorder.Body.String(), "FreeUsageLimitError")
	require.NotContains(t, recorder.Body.String(), "Free usage limit reached")
	repo := svc.accountRepo.(*openAIAccountTestRepo)
	require.Equal(t, int64(401), repo.rateLimitedID)
	require.NotNil(t, repo.rateLimitedAt)
	// The upstream Retry-After (74005s) over-states the block and is capped.
	require.WithinDuration(t, time.Now().Add(openCodeFreeQuotaCooldownMax), *repo.rateLimitedAt, 30*time.Second)
}
