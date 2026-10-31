package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type freeWorkerProxy struct {
	ID         int64  `json:"id"`
	Generation string `json:"generation"`
	URL        string `json:"url"`
}
type freeWorkerEnvelope struct {
	Version        int              `json:"version"`
	RequestID      string           `json:"request_id"`
	ProfileID      string           `json:"profile_id"`
	ProfileSHA256  string           `json:"profile_sha256"`
	Target         string           `json:"target"`
	AccountID      int64            `json:"account_id"`
	SessionID      string           `json:"session_id"`
	DeadlineUnixMS int64            `json:"deadline_unix_ms"`
	Proxy          *freeWorkerProxy `json:"proxy,omitempty"`
	Body           json.RawMessage  `json:"body"`
}

// openCodeFreeClientRetryAfterCap bounds the Retry-After the gateway exposes
// to clients. The upstream free-quota 429 over-states the block (observed
// values of 8+ hours while instances served 200 again within seconds), and a
// client that honors such a value sleeps through recoverable windows; 120s
// matches the observed flap/recovery timescale of the free pool and keeps the
// client-facing hint honest without touching the (capped, verified) internal
// cooldown, which still uses the real upstream value.
const openCodeFreeClientRetryAfterCap = 120 * time.Second

// openCodeFreeClientRetryAfter caps a delta-seconds Retry-After for client
// exposure. Unparseable values (HTTP-date form or malformed) pass through
// unchanged: the cap must never invent a value or break the header contract.
func openCodeFreeClientRetryAfter(retry string) string {
	if retry == "" {
		return ""
	}
	seconds, err := strconv.Atoi(retry)
	if err != nil {
		return retry
	}
	if seconds <= 0 || time.Duration(seconds)*time.Second <= openCodeFreeClientRetryAfterCap {
		return retry
	}
	return strconv.Itoa(int(openCodeFreeClientRetryAfterCap / time.Second))
}

func freeReject(c *gin.Context, status int, message string) error {
	if !c.Writer.Written() {
		writeChatCompletionsError(c, status, "invalid_request_error", message)
	} else {
		b, _ := json.Marshal(gin.H{"error": gin.H{"type": "api_error", "message": message}})
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		c.Writer.Flush()
		MarkResponseCommitted(c)
	}
	return errors.New(message)
}

// The descending six-byte timestamp follows OpenCode's identifier.ts. A
// random fourteen-byte suffix provides isolation even when requests share a millisecond.
func formatOpenCodeFreeSession(unixMillis int64, entropy [14]byte) string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	descending := ^((uint64(unixMillis) << 12) + 1) & 0xffffffffffff
	var s strings.Builder
	fmt.Fprintf(&s, "ses_%012x", descending)
	for _, v := range entropy {
		_ = s.WriteByte(chars[int(v)%len(chars)])
	}
	return s.String()
}

func freeSessionID() (string, error) {
	var entropy [14]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return formatOpenCodeFreeSession(time.Now().UnixMilli(), entropy), nil
}

func (s *OpenAIGatewayService) forwardOpenCodeFree(ctx context.Context, c *gin.Context, a *Account, body []byte, defaultModel string) (*OpenAIForwardResult, error) {
	start := time.Now()
	cfg, err := openCodeFreeConfig(s.cfg)
	if err != nil {
		return nil, freeReject(c, http.StatusServiceUnavailable, err.Error())
	}
	if validateOpenCodeFreeAccount(a) != nil {
		return nil, freeReject(c, http.StatusBadRequest, "invalid OpenCode free account configuration")
	}
	model := gjson.GetBytes(body, "model").String()
	displayModel := model
	if original := c.GetString("opencode_free_original_model"); original != "" {
		displayModel = original
	}
	mapped := resolveOpenAIForwardModel(a, model, defaultModel)
	reqNorm, err := normalizeOpenCodeFreeRequest(body, mapped)
	if err != nil {
		return nil, freeReject(c, http.StatusBadRequest, err.Error())
	}
	stream := reqNorm.Stream
	keyID := getAPIKeyIDFromContext(c)
	if keyID <= 0 {
		return nil, freeReject(c, http.StatusServiceUnavailable, "OpenCode free identity unavailable")
	}
	session, err := freeSessionID()
	if err != nil {
		return nil, freeReject(c, http.StatusServiceUnavailable, "session creation failed")
	}
	if explicit := ExtractClientSessionID(c); explicit != "" {
		// Token rotation intentionally starts a new session namespace.
		mac := hmac.New(sha256.New, []byte(cfg.Token))
		fmt.Fprintf(mac, "opencode-free-session-v1\x00%d\x00%d\x00%d\x00%s", keyID, a.ID, len(explicit), explicit)
		session, err = s.ResolveOpenCodeFreeSession(ctx, hex.EncodeToString(mac.Sum(nil)), session, 24*time.Hour)
		if err != nil {
			return nil, freeReject(c, http.StatusServiceUnavailable, "session storage unavailable")
		}
	}
	proxy, err := openCodeFreeProxy(a)
	if err != nil {
		return nil, freeReject(c, http.StatusBadRequest, err.Error())
	}
	timeout := time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	meta, err := s.BeginOpenCodeFreeAttempt(requestCtx, keyID, a.ID, reqNorm.Profile.SHA256, HashUsageRequestPayload(body), timeout)
	if err != nil {
		return nil, freeReject(c, http.StatusServiceUnavailable, "OpenCode free accounting unavailable")
	}
	complete := false
	defer func() {
		if !complete {
			dbCtx, dbCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer dbCancel()
			_ = s.MarkOpenCodeFreeUnknown(dbCtx, meta, "incomplete")
		}
	}()
	deadline, _ := requestCtx.Deadline()
	env := freeWorkerEnvelope{Version: 1, RequestID: meta.RequestID, ProfileID: reqNorm.Profile.ID, ProfileSHA256: reqNorm.Profile.SHA256, Target: "console", AccountID: a.ID, SessionID: session, DeadlineUnixMS: deadline.UnixMilli(), Proxy: proxy, Body: reqNorm.Body}
	resp, err := dispatchOpenCodeFree(requestCtx, cfg, env)
	if err != nil {
		logger.L().Warn("opencode free: worker transport failed",
			zap.String("request_id", meta.RequestID),
			zap.Int64("account_id", a.ID),
			zap.String("model", model),
			zap.Bool("stream", stream),
			zap.Duration("elapsed", time.Since(start)),
			zap.NamedError("context", requestCtx.Err()),
			zap.Error(err))
		return nil, freeReject(c, http.StatusBadGateway, "OpenCode free transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	source, dispatched := resp.Header.Get("X-Sub2api-Transport-Result"), resp.Header.Get("X-Sub2api-Dispatched")
	if source != "upstream" || dispatched != "true" {
		// Worker-side failures discard the transport evidence by design; the
		// worker logs the upstream status itself, this records the code the
		// gateway actually received so a bare502 can be explained.
		workerBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
		workerCode := gjson.GetBytes(workerBody, "error.code").String()
		workerMessage := gjson.GetBytes(workerBody, "error.message").String()
		logger.L().Warn("opencode free: worker error",
			zap.String("request_id", meta.RequestID),
			zap.Int64("account_id", a.ID),
			zap.String("model", model),
			zap.Int("worker_status", resp.StatusCode),
			zap.String("transport_result", source),
			zap.String("dispatched", dispatched),
			zap.String("worker_code", workerCode),
			zap.String("worker_message", redactOpenCodeFreeLogSnippet(workerMessage)),
			zap.Duration("elapsed", time.Since(start)))
		clientMessage := "OpenCode free transport failed"
		if workerCode != "" {
			clientMessage += ": " + workerCode
		}
		return nil, freeReject(c, http.StatusBadGateway, clientMessage)
	}
	if resp.StatusCode != http.StatusOK {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		if readErr != nil || len(data) > 65536 || !json.Valid(data) {
			logger.L().Warn("opencode free: upstream error body unreadable",
				zap.String("request_id", meta.RequestID),
				zap.Int64("account_id", a.ID),
				zap.String("model", model),
				zap.Int("upstream_status", resp.StatusCode),
				zap.String("retry_after", resp.Header.Get("Retry-After")),
				zap.String("upstream_request_id", resp.Header.Get("X-Request-Id")),
				zap.Int("body_bytes", len(data)),
				zap.Duration("elapsed", time.Since(start)),
				zap.NamedError("read_error", readErr))
			return nil, freeReject(c, http.StatusBadGateway, "OpenCode free upstream failed")
		}
		upstreamMsg := ExtractUpstreamErrorMessage(data)
		logger.L().Warn("opencode free: upstream error",
			zap.String("request_id", meta.RequestID),
			zap.Int64("account_id", a.ID),
			zap.String("model", model),
			zap.String("display_model", displayModel),
			zap.Int("upstream_status", resp.StatusCode),
			zap.String("retry_after", resp.Header.Get("Retry-After")),
			zap.String("upstream_request_id", resp.Header.Get("X-Request-Id")),
			zap.String("content_type", resp.Header.Get("Content-Type")),
			zap.String("upstream_message", redactOpenCodeFreeLogSnippet(upstreamMsg)),
			zap.String("body_snippet", openCodeFreeBodySnippet(data)),
			zap.Bool("stream", stream),
			zap.Bool("response_committed", c.Writer.Written()),
			zap.Duration("elapsed", time.Since(start)))
		if isOpenCodeFreeQuotaExhausted(resp.StatusCode, data) {
			// Free quota is per egress identity: park the instance for its
			// window, then let the handler switch to another instance of the
			// same group while nothing has been written to the client yet.
			cooldownCtx, cooldownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleOpenCodeFreeQuota429(cooldownCtx, a, resp.Header)
				// An upstream retry-after over-states the real block; verify the
				// park asynchronously and auto-clear it when upstream serves 200
				// again so a healthy instance is not lost for the whole window.
				s.rateLimitService.ScheduleOpenCodeFreeQuotaVerify(a, mapped, cfg)
			}
			cooldownCancel()
			if !c.Writer.Written() {
				logger.L().Warn("opencode free: quota exhausted, failing over",
					zap.String("request_id", meta.RequestID),
					zap.Int64("account_id", a.ID),
					zap.String("model", model),
					zap.String("retry_after", resp.Header.Get("Retry-After")))
				// The cooldown above keeps the real upstream Retry-After; the
				// client-facing copy is capped so callers retry within the
				// window where the free pool demonstrably recovers.
				clientHeaders := resp.Header.Clone()
				if retry := clientHeaders.Get("Retry-After"); retry != "" {
					clientHeaders.Set("Retry-After", openCodeFreeClientRetryAfter(retry))
				}
				return nil, s.newOpenAIAccountFailoverError(a, resp.StatusCode, clientHeaders, data, upstreamMsg, false, false)
			}
		}
		if stream && c.Writer.Written() {
			// A queue keepalive may already have committed the SSE response.
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				return nil, freeReject(c, http.StatusBadGateway, "OpenCode free upstream failed")
			}
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", compact.Bytes())
			c.Writer.Flush()
			MarkResponseCommitted(c)
		} else {
			if retry := resp.Header.Get("Retry-After"); retry != "" {
				c.Header("Retry-After", openCodeFreeClientRetryAfter(retry))
			}
			c.Data(resp.StatusCode, "application/json", data)
			MarkResponseCommitted(c)
		}
		return nil, errors.New("OpenCode free upstream error")
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		logger.L().Warn("opencode free: invalid upstream content type",
			zap.String("request_id", meta.RequestID),
			zap.Int64("account_id", a.ID),
			zap.String("model", model),
			zap.String("content_type", resp.Header.Get("Content-Type")),
			zap.Duration("elapsed", time.Since(start)))
		return nil, freeReject(c, http.StatusBadGateway, "invalid upstream content type")
	}
	var out io.Writer
	if stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		out = c.Writer
	}
	parsed, err := parseOpenCodeFreeSSE(requestCtx, resp.Body, out, cfg.MaxResponseBytes, cfg.MaxEventBytes, displayModel, &openCodeFreeSSEOptions{ToolNames: reqNorm.ToolNames, ToolChoice: reqNorm.ToolChoice, InjectedTools: reqNorm.InjectedTools})
	if err != nil || requestCtx.Err() != nil || parsed.BillingSource != "free" {
		logger.L().Warn("opencode free: incomplete upstream stream",
			zap.String("request_id", meta.RequestID),
			zap.String("model", model),
			zap.String("profile_id", reqNorm.Profile.ID),
			zap.String("billing_source", func() string {
				if parsed == nil {
					return ""
				}
				return parsed.BillingSource
			}()),
			zap.NamedError("context", requestCtx.Err()),
			zap.Error(err),
		)
		return nil, freeReject(c, http.StatusBadGateway, "incomplete OpenCode free response")
	}
	meta.RawUsage = freeSafeUsage(parsed)
	meta.BillingSource = parsed.BillingSource
	meta.Complete = true
	if !stream {
		message := gin.H{"role": "assistant", "content": parsed.Text}
		if parsed.Reasoning != "" {
			message["reasoning_content"] = parsed.Reasoning
		}
		if len(parsed.ToolCalls) > 0 {
			if parsed.Text == "" {
				message["content"] = nil
			}
			message["tool_calls"] = parsed.ToolCalls
		}
		response, err := json.Marshal(gin.H{"id": parsed.ID, "object": "chat.completion", "created": time.Now().Unix(), "model": displayModel, "choices": []any{gin.H{"index": 0, "message": message, "finish_reason": parsed.Finish}}, "usage": json.RawMessage(parsed.RawUsage)})
		if err != nil {
			return nil, freeReject(c, http.StatusBadGateway, "cannot encode OpenCode free response")
		}
		if requestCtx.Err() != nil {
			return nil, errors.New("OpenCode free response cancelled")
		}
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.Writer.WriteHeader(http.StatusOK)
		n, err := c.Writer.Write(response)
		if err != nil || n != len(response) || requestCtx.Err() != nil {
			return nil, errors.New("OpenCode free response write failed")
		}
	}
	complete = true
	SetActualOpenAIUpstreamEndpoint(c, "/v1/chat/completions")
	return &OpenAIForwardResult{RequestID: meta.RequestID, ResponseID: parsed.ID, Usage: parsed.Usage, Model: displayModel, BillingModel: mapped, UpstreamModel: mapped, UpstreamEndpoint: "/v1/chat/completions", Stream: stream, Duration: time.Since(start), OpenCodeFree: meta}, nil
}

// openCodeFreeConfig, openCodeFreeProxy and dispatchOpenCodeFree are shared by
// the gateway and the admin diagnostic so they use the same transport boundary.
func openCodeFreeConfig(cfg *config.Config) (config.OpenCodeFreeBunConfig, error) {
	if cfg == nil || !cfg.OpenCodeFreeBun.Enabled {
		return config.OpenCodeFreeBunConfig{}, errors.New("OpenCode free transport is disabled")
	}
	free := cfg.OpenCodeFreeBun
	endpoint, err := url.Parse(free.WorkerEndpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || len(free.Token) < 32 || free.RequestTimeoutSeconds <= 0 || free.MaxResponseBytes <= 0 || free.MaxEventBytes <= 0 {
		return config.OpenCodeFreeBunConfig{}, errors.New("invalid OpenCode free transport configuration")
	}
	return free, nil
}

func openCodeFreeProxy(a *Account) (*freeWorkerProxy, error) {
	if a.ProxyID == nil {
		return nil, nil
	}
	if a.Proxy == nil || a.Proxy.ID != *a.ProxyID || a.Proxy.Protocol != "http" {
		return nil, errors.New("unsupported OpenCode free proxy")
	}
	raw := a.Proxy.URL()
	hash := sha256.Sum256([]byte(raw))
	return &freeWorkerProxy{ID: a.Proxy.ID, Generation: hex.EncodeToString(hash[:]), URL: raw}, nil
}

func dispatchOpenCodeFree(ctx context.Context, cfg config.OpenCodeFreeBunConfig, env freeWorkerEnvelope) (*http.Response, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.WorkerEndpoint, "/")+"/v1/transport/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	return client.Do(req)
}

// Persist only numeric usage fields and the known billing source, never arbitrary upstream data.
func freeSafeUsage(parsed *openCodeFreeParsed) json.RawMessage {
	raw := parsed.RawUsage
	var in map[string]json.RawMessage
	_ = json.Unmarshal(raw, &in)
	out := map[string]json.RawMessage{}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		if v, ok := in[key]; ok {
			var n int64
			if json.Unmarshal(v, &n) == nil && n >= 0 && string(v) != "null" {
				out[key] = v
			}
		}
	}
	for _, pair := range []struct {
		key    string
		fields []string
	}{
		{"prompt_tokens_details", []string{"cached_tokens", "cache_creation_tokens"}},
		{"completion_tokens_details", []string{"reasoning_tokens"}},
	} {
		var details map[string]json.RawMessage
		if json.Unmarshal(in[pair.key], &details) == nil && details != nil {
			safe := map[string]json.RawMessage{}
			for _, k := range pair.fields {
				if v, ok := details[k]; ok {
					var n int64
					if json.Unmarshal(v, &n) == nil && n >= 0 && string(v) != "null" {
						safe[k] = v
					}
				}
			}
			if len(safe) > 0 {
				b, _ := json.Marshal(safe)
				out[pair.key] = b
			}
		}
	}
	if v, ok := in["billing_source"]; ok {
		var source string
		if json.Unmarshal(v, &source) == nil && source == "free" {
			out["billing_source"] = v
		}
	}
	out["billing_source"], _ = json.Marshal(parsed.BillingSource)
	out["cache_read_input_tokens"], _ = json.Marshal(parsed.Usage.CacheReadInputTokens)
	out["cache_creation_input_tokens"], _ = json.Marshal(parsed.Usage.CacheCreationInputTokens)
	var oc struct {
		Tokens map[string]json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(in["opencode"], &oc) == nil && oc.Tokens != nil {
		safe := map[string]json.RawMessage{}
		for _, k := range []string{"input", "output", "reasoning", "cache_read", "cache_write_5m", "cache_write_1h"} {
			if v, ok := oc.Tokens[k]; ok {
				var n int64
				if json.Unmarshal(v, &n) == nil && n >= 0 && string(v) != "null" {
					safe[k] = v
				}
			}
		}
		obj, _ := json.Marshal(map[string]any{"billing_source": parsed.BillingSource, "tokens": safe})
		out["opencode"] = obj
	}
	b, _ := json.Marshal(out)
	return b
}
