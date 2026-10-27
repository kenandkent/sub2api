package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A marked account must fail closed even when the worker is disabled; it must
// never reach the ordinary upstream transport or trigger account failover.
func TestOpenCodeFreeMarkedAccountDoesNotFallBackWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`))
	a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile}}
	svc := &OpenAIGatewayService{}
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`), "", "")
	require.Nil(t, result)
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
	require.Equal(t, 503, w.Code)
}

type freeGatewayRepo struct {
	UsageBillingRepository
	unknown  int
	sessions map[string]string
}

func (r *freeGatewayRepo) BeginOpenCodeFreeAttempt(context.Context, *OpenCodeFreeUsageMeta, int64, int64, string, string, time.Duration) error {
	return nil
}
func (r *freeGatewayRepo) MarkOpenCodeFreeUnknown(context.Context, *OpenCodeFreeUsageMeta, string) error {
	r.unknown++
	return nil
}
func (r *freeGatewayRepo) ResolveOpenCodeFreeSession(_ context.Context, key, candidate string, _ time.Duration) (string, error) {
	if r.sessions == nil {
		r.sessions = map[string]string{}
	}
	if session := r.sessions[key]; session != "" {
		return session, nil
	}
	r.sessions[key] = candidate
	return candidate, nil
}
func (r *freeGatewayRepo) StageOpenCodeFreeUsage(context.Context, *OpenCodeFreeUsageMeta, *UsageBillingCommand, *UsageLog, *OpenCodeFreeBillingContext) error {
	return nil
}
func (r *freeGatewayRepo) SettleOpenCodeFreeUsage(context.Context, string) (*OpenCodeFreeSettlement, error) {
	return nil, nil
}
func (r *freeGatewayRepo) RecoverOpenCodeFreeUsage(context.Context) ([]OpenCodeFreeRecoveredUsage, error) {
	return nil, nil
}

func TestOpenCodeFreeUpstreamStatusDoesNotFailOver(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer 12345678901234567890123456789012", r.Header.Get("Authorization"))
		var env freeWorkerEnvelope
		require.NoError(t, json.NewDecoder(r.Body).Decode(&env))
		require.Equal(t, "big-pickle", gjson.GetBytes(env.Body, "model").String())
		require.Equal(t, "console", env.Target)
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"try later"}}`))
	}))
	defer worker.Close()
	repo := &freeGatewayRepo{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
	a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{ID: 4})
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`), "", "")
	require.Nil(t, result)
	require.Error(t, err)
	var f *UpstreamFailoverError
	require.NotErrorAs(t, err, &f)
	require.Equal(t, 429, w.Code)
	require.Equal(t, "12", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "rate_limit")
	require.Equal(t, 1, repo.unknown)
}

func TestOpenCodeFreeSocketCompletesStreamingAndBufferedWithAlias(t *testing.T) {
	fixture := "data: {\"id\":\"answer\",\"object\":\"chat.completion.chunk\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"answer\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"answer\",\"model\":\"big-pickle\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"billing_source\":\"free\",\"sensitive\":\"do not persist\"}}\n\n" + "data: [DONE]\n\n"
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env freeWorkerEnvelope
		require.NoError(t, json.NewDecoder(r.Body).Decode(&env))
		require.Equal(t, "big-pickle", gjson.GetBytes(env.Body, "model").String())
		require.Equal(t, "user", gjson.GetBytes(env.Body, "messages.0.role").String())
		require.Equal(t, "hi", gjson.GetBytes(env.Body, "messages.0.content").String())
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		_, _ = w.Write([]byte(fixture))
	}))
	defer worker.Close()
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			repo := &freeGatewayRepo{}
			svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
			a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile, "model_mapping": map[string]any{"my-alias": "big-pickle"}}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			c.Set("api_key", &APIKey{ID: 4})
			body := []byte(fmt.Sprintf(`{"model":"my-alias","messages":[{"role":"user","content":"hi"}],"stream":%t}`, stream))
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, a, body, "", "")
			require.NoError(t, err)
			require.Equal(t, "my-alias", result.Model)
			require.Equal(t, 10, result.Usage.InputTokens)
			require.True(t, result.OpenCodeFree.Complete)
			require.NotContains(t, string(result.OpenCodeFree.RawUsage), "sensitive")
			require.Equal(t, 0, repo.unknown)
			require.Contains(t, w.Body.String(), `"model":"my-alias"`)
			require.Contains(t, w.Body.String(), "hello")
			if stream {
				require.Contains(t, w.Body.String(), "[DONE]")
			} else {
				require.Equal(t, "hello", gjson.Get(w.Body.String(), "choices.0.message.content").String())
			}
		})
	}
}

func TestOpenCodeFreePreservesAliasAfterChannelMapping(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		_, _ = w.Write([]byte("data: {\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n"))
	}))
	defer worker.Close()
	repo := &freeGatewayRepo{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
	a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{ID: 4})
	c.Set("opencode_free_original_model", "alias-from-channel")
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`), "", "")
	require.NoError(t, err)
	require.Equal(t, "alias-from-channel", result.Model)
	require.Contains(t, w.Body.String(), `"model":"alias-from-channel"`)
}

func TestOpenCodeFreeMarkerDoesNotAffectOtherPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformDeepseek, PlatformGrok} {
		a := &Account{Platform: platform, Credentials: map[string]any{"opencode_free_transport": "bun"}}
		require.False(t, a.UsesOpenCodeFreeBun(), platform)
	}
}

func TestOpenCodeFreeCancelStopsUpstreamAndMarksUnknown(t *testing.T) {
	started := make(chan struct{})
	ended := make(chan struct{})
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(ended)
	}))
	defer worker.Close()
	repo := &freeGatewayRepo{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
	a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	c.Set("api_key", &APIKey{ID: 4})
	type outcome struct {
		res *OpenAIForwardResult
		err error
	}
	result := make(chan outcome, 1)
	go func() {
		res, err := svc.ForwardAsChatCompletions(ctx, c, a, []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`), "", "")
		result <- outcome{res, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never started")
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("worker was not canceled")
	}
	select {
	case got := <-result:
		require.Nil(t, got.res)
		require.Error(t, got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not stop")
	}
	require.Equal(t, 1, repo.unknown)
	require.NotContains(t, w.Body.String(), "[DONE]")
}

func TestOpenCodeFreeSessionIsScopedToKeyAndAccount(t *testing.T) {
	var sessions []string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope freeWorkerEnvelope
		require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
		sessions = append(sessions, envelope.SessionID)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n"))
	}))
	defer worker.Close()
	repo := &freeGatewayRepo{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
	send := func(key, account int64, explicit bool) {
		a := &Account{ID: account, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile}}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if explicit {
			c.Request.Header.Set("Session-Id", "conversation")
		}
		c.Set("api_key", &APIKey{ID: key})
		_, err := svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`), "", "")
		require.NoError(t, err)
	}
	send(1, 1, true)
	send(1, 1, true)
	send(2, 1, true)
	send(1, 2, true)
	send(1, 1, false)
	send(1, 1, false)
	require.Equal(t, sessions[0], sessions[1])
	require.NotEqual(t, sessions[0], sessions[2])
	require.NotEqual(t, sessions[0], sessions[3])
	require.NotEqual(t, sessions[4], sessions[5])
	for _, s := range sessions {
		require.Regexp(t, `^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`, s)
	}
}

func TestOpenCodeFreeRejectsNonChatEntrypoints(t *testing.T) {
	a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"opencode_free_transport": "bun"}}
	svc := &OpenAIGatewayService{}
	cases := map[string]func(*gin.Context) error{
		"responses": func(c *gin.Context) error {
			_, err := svc.Forward(context.Background(), c, a, []byte(`{}`))
			return err
		},
		"messages": func(c *gin.Context) error {
			_, err := svc.ForwardAsAnthropic(context.Background(), c, a, []byte(`{}`), "", "")
			return err
		},
		"images": func(c *gin.Context) error {
			_, err := svc.ForwardImages(context.Background(), c, a, nil, nil, "")
			return err
		},
		"embeddings": func(c *gin.Context) error {
			_, err := svc.ForwardEmbeddings(context.Background(), c, a, []byte(`{}`), "")
			return err
		},
		"alpha-search": func(c *gin.Context) error {
			_, err := svc.ForwardAlphaSearch(context.Background(), c, a, []byte(`{}`))
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/", nil)
			err := run(c)
			require.Error(t, err)
			var f *UpstreamFailoverError
			require.NotErrorAs(t, err, &f)
			require.Equal(t, 400, w.Code)
		})
	}
}

func TestOpenCodeFreeSessionMatchesNativeDescendingGolden(t *testing.T) {
	// Source: .cache/opencode-client-audit/packages/schema/src/identifier.ts,
	// timestamp 1793084105000 ms, first counter=1 and bytes 0..13.
	var entropy [14]byte
	for i := range entropy {
		entropy[i] = byte(i)
	}
	require.Equal(t, "ses_e83dbded7ffe0123456789ABCD", formatOpenCodeFreeSession(1793084105000, entropy))
}

type freeFailingResponseWriter struct {
	header http.Header
	status int
}

func (w *freeFailingResponseWriter) Header() http.Header    { return w.header }
func (w *freeFailingResponseWriter) WriteHeader(status int) { w.status = status }
func (w *freeFailingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("client write failed")
}

func TestOpenCodeFreeBufferedWriteFailureMarksUnknown(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n"))
	}))
	defer worker.Close()
	repo := &freeGatewayRepo{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
	a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile}}
	w := &freeFailingResponseWriter{header: make(http.Header)}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{ID: 4})
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`), "", "")
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, 1, repo.unknown)
}

func TestOpenCodeFreeUpstreamErrorAfterKeepaliveIsSSE(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"try later"}}`))
	}))
	defer worker.Close()
	repo := &freeGatewayRepo{}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, cfg: &config.Config{OpenCodeFreeBun: config.OpenCodeFreeBunConfig{Enabled: true, WorkerEndpoint: worker.URL, Token: "12345678901234567890123456789012", RequestTimeoutSeconds: 30, MaxResponseBytes: 1 << 20, MaxEventBytes: 1 << 16}}}
	a := &Account{ID: 1, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": AccountModeZen, "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": openCodeFreeProfile}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{ID: 4})
	c.Header("Content-Type", "text/event-stream")
	_, _ = c.Writer.WriteString(": keepalive\n\n")
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}],"stream":true}`), "", "")
	require.Nil(t, result)
	require.Error(t, err)
	var f *UpstreamFailoverError
	require.NotErrorAs(t, err, &f)
	require.Equal(t, 1, repo.unknown)
	require.Contains(t, w.Body.String(), "data: {\"error\":")
	require.Contains(t, w.Body.String(), "rate_limit")
	require.True(t, strings.HasSuffix(w.Body.String(), "\n\n"))
	require.NotContains(t, w.Body.String(), "[DONE]")
}
