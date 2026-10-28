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
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
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

func freeReject(c *gin.Context, status int, message string) error {
	if !c.Writer.Written() {
		writeChatCompletionsError(c, status, "invalid_request_error", message)
	} else {
		b, _ := json.Marshal(gin.H{"error": gin.H{"type": "api_error", "message": message}})
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		c.Writer.Flush()
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
	if s.cfg == nil || !s.cfg.OpenCodeFreeBun.Enabled {
		return nil, freeReject(c, http.StatusServiceUnavailable, "OpenCode free transport is disabled")
	}
	cfg := s.cfg.OpenCodeFreeBun
	endpoint, err := url.Parse(cfg.WorkerEndpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || len(cfg.Token) < 32 || cfg.RequestTimeoutSeconds <= 0 || cfg.MaxResponseBytes <= 0 || cfg.MaxEventBytes <= 0 {
		return nil, freeReject(c, http.StatusServiceUnavailable, "invalid OpenCode free transport configuration")
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
	normalized, stream, err := normalizeOpenCodeFreeRequest(body, mapped)
	if err != nil {
		return nil, freeReject(c, http.StatusBadRequest, err.Error())
	}
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
	var proxy *freeWorkerProxy
	if a.ProxyID != nil {
		if a.Proxy == nil || a.Proxy.ID != *a.ProxyID || a.Proxy.Protocol != "http" {
			return nil, freeReject(c, http.StatusBadRequest, "unsupported OpenCode free proxy")
		}
		raw := a.Proxy.URL()
		hash := sha256.Sum256([]byte(raw))
		proxy = &freeWorkerProxy{ID: a.Proxy.ID, Generation: hex.EncodeToString(hash[:]), URL: raw}
	}
	timeout := time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	meta, err := s.BeginOpenCodeFreeAttempt(requestCtx, keyID, a.ID, openCodeFreeProfileHash, HashUsageRequestPayload(body), timeout)
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
	env := freeWorkerEnvelope{Version: 1, RequestID: meta.RequestID, ProfileID: openCodeFreeProfile, ProfileSHA256: openCodeFreeProfileHash, Target: "console", AccountID: a.ID, SessionID: session, DeadlineUnixMS: deadline.UnixMilli(), Proxy: proxy, Body: normalized}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, freeReject(c, http.StatusBadGateway, "cannot encode transport request")
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(cfg.WorkerEndpoint, "/")+"/v1/transport/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, freeReject(c, http.StatusBadGateway, "invalid transport request")
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, freeReject(c, http.StatusBadGateway, "OpenCode free transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	source, dispatched := resp.Header.Get("X-Sub2api-Transport-Result"), resp.Header.Get("X-Sub2api-Dispatched")
	if source != "upstream" || dispatched != "true" {
		return nil, freeReject(c, http.StatusBadGateway, "OpenCode free transport failed")
	}
	if resp.StatusCode != http.StatusOK {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		if readErr != nil || len(data) > 65536 || !json.Valid(data) {
			return nil, freeReject(c, http.StatusBadGateway, "OpenCode free upstream failed")
		}
		if stream && c.Writer.Written() {
			// A queue keepalive may already have committed the SSE response.
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				return nil, freeReject(c, http.StatusBadGateway, "OpenCode free upstream failed")
			}
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", compact.Bytes())
			c.Writer.Flush()
		} else {
			if retry := resp.Header.Get("Retry-After"); retry != "" {
				c.Header("Retry-After", retry)
			}
			c.Data(resp.StatusCode, "application/json", data)
		}
		return nil, errors.New("OpenCode free upstream error")
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return nil, freeReject(c, http.StatusBadGateway, "invalid upstream content type")
	}
	var out io.Writer
	if stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		out = c.Writer
	}
	parsed, err := parseOpenCodeFreeSSE(requestCtx, resp.Body, out, cfg.MaxResponseBytes, cfg.MaxEventBytes, displayModel)
	if err != nil || requestCtx.Err() != nil || parsed.BillingSource != "free" {
		return nil, freeReject(c, http.StatusBadGateway, "incomplete OpenCode free response")
	}
	meta.RawUsage = freeSafeUsage(parsed)
	meta.BillingSource = parsed.BillingSource
	meta.Complete = true
	if !stream {
		response, err := json.Marshal(gin.H{"id": parsed.ID, "object": "chat.completion", "created": time.Now().Unix(), "model": displayModel, "choices": []any{gin.H{"index": 0, "message": gin.H{"role": "assistant", "content": parsed.Text}, "finish_reason": parsed.Finish}}, "usage": json.RawMessage(parsed.RawUsage)})
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
