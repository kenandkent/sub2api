package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// A diagnostic has its own request and session IDs; it has no consumer key and
// creates no billing attempt. It otherwise uses the gateway's transport and
// response validator, so the result represents the same upstream path.
func (s *AccountTestService) testOpenCodeFreeBunConnection(c *gin.Context, account *Account, model, prompt string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultOpenCodeFreeTestModel(account)
	}
	mapped := resolveOpenAIForwardModel(account, model, "")
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	s.sendEvent(c, TestEvent{Type: "test_start", Model: mapped})

	cfg, err := openCodeFreeConfig(s.cfg)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	if validateOpenCodeFreeAccount(account) != nil {
		return s.sendErrorAndEnd(c, "invalid OpenCode free account configuration")
	}
	proxy, err := openCodeFreeProxy(account)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	body, err := json.Marshal(createOpenAIChatCompletionsTestPayload(model, prompt))
	if err != nil {
		return s.sendErrorAndEnd(c, "cannot encode OpenCode free test")
	}
	normalized, err := normalizeOpenCodeFreeRequest(body, mapped)
	if err != nil {
		return s.sendErrorAndEnd(c, "invalid OpenCode free test request")
	}
	requestID, err := freeRandomID("ocfree_")
	if err != nil {
		return s.sendErrorAndEnd(c, "OpenCode free request ID creation failed")
	}
	session, err := freeSessionID()
	if err != nil {
		return s.sendErrorAndEnd(c, "OpenCode free session creation failed")
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	env := freeWorkerEnvelope{Version: 1, RequestID: requestID, ProfileID: normalized.Profile.ID, ProfileSHA256: normalized.Profile.SHA256, Target: "console", AccountID: account.ID, SessionID: session, DeadlineUnixMS: deadline.UnixMilli(), Proxy: proxy, Body: normalized.Body}
	// Include only generated identity and known profile in diagnostics: worker
	// errors and response bodies can contain credentials and must stay private.
	identity := redactOpenCodeFreeTestContent(fmt.Sprintf("request_id=%s profile=%s", requestID, normalized.Profile.ID), cfg.Token, account.Proxy)
	resp, err := dispatchOpenCodeFree(ctx, cfg, env)
	if err != nil {
		return s.sendErrorAndEnd(c, "OpenCode free transport failed ("+identity+")")
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Sub2api-Transport-Result") != "upstream" || resp.Header.Get("X-Sub2api-Dispatched") != "true" {
		return s.sendErrorAndEnd(c, fmt.Sprintf("OpenCode free worker failed (HTTP %d, %s)", resp.StatusCode, identity))
	}
	if resp.StatusCode != http.StatusOK {
		// Read the error body only for local classification; it never reaches
		// the admin stream (worker/upstream bodies can carry credentials).
		// An exhausted free-tier instance is parked like in the gateway so the
		// remaining test rounds and live traffic route to healthy instances.
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		if isOpenCodeFreeQuotaExhausted(resp.StatusCode, data) {
			cooldownCtx, cooldownCancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
			if s.openaiGatewayService != nil && s.openaiGatewayService.rateLimitService != nil {
				s.openaiGatewayService.rateLimitService.HandleOpenCodeFreeQuota429(cooldownCtx, account, resp.Header)
			} else {
				applyOpenCodeFreeQuotaCooldown(cooldownCtx, s.accountRepo, account, resp.Header, nil)
			}
			cooldownCancel()
		}
		return s.sendErrorAndEnd(c, fmt.Sprintf("OpenCode free upstream failed (HTTP %d, %s)", resp.StatusCode, identity))
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return s.sendErrorAndEnd(c, "invalid OpenCode free upstream content type ("+identity+")")
	}
	parsed, err := parseOpenCodeFreeSSE(ctx, resp.Body, nil, cfg.MaxResponseBytes, cfg.MaxEventBytes, mapped, &openCodeFreeSSEOptions{ToolNames: normalized.ToolNames, ToolChoice: normalized.ToolChoice, InjectedTools: normalized.InjectedTools, QuietErrors: true})
	if err != nil || ctx.Err() != nil || parsed.BillingSource != "free" {
		return s.sendErrorAndEnd(c, "incomplete OpenCode free response ("+identity+")")
	}
	if parsed.Text != "" {
		s.sendEvent(c, TestEvent{Type: "content", Text: redactOpenCodeFreeTestContent(parsed.Text, cfg.Token, account.Proxy)})
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// Pick a concrete supported request model deterministically, including a mapped
// target or a representative request for a trailing wildcard. Verify the
// effective mapping because a more-specific entry may override a candidate.
func defaultOpenCodeFreeTestModel(account *Account) string {
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return "big-pickle"
	}
	var candidates []string
	for model, mapped := range mapping {
		possible := []string{mapped}
		if strings.HasSuffix(model, "*") {
			possible = append(possible, openCodeFreeWildcardRepresentative(account, strings.TrimSuffix(model, "*"), mapping))
		} else {
			possible = append(possible, model)
		}
		for _, candidate := range possible {
			if candidate != "" && account.IsModelSupported(candidate) && openCodeFreeAdmissible(account.GetMappedModel(candidate)) {
				candidates = append(candidates, candidate)
			}
		}
	}
	sort.Strings(candidates)
	if len(candidates) > 0 {
		return candidates[0]
	}
	if account.IsModelSupported("big-pickle") && openCodeFreeAdmissible(account.GetMappedModel("big-pickle")) {
		return "big-pickle"
	}
	return ""
}

// Mapping wildcards are trailing-star prefixes. Find a concrete request in the
// prefix's region that no configured longer key shadows, then validate the
// effective mapping. Only visit existing child prefixes if no fresh branch is
// available or if its effective target is not admissible.
func openCodeFreeWildcardRepresentative(account *Account, prefix string, mapping map[string]string) string {
	var visit func(string) string
	visit = func(candidate string) string {
		_, patternLiteral := mapping[candidate]
		if candidate != "" && !(patternLiteral && strings.HasSuffix(candidate, "*")) && account.IsModelSupported(candidate) && openCodeFreeAdmissible(account.GetMappedModel(candidate)) {
			return candidate
		}
		children := make(map[rune]bool)
		for key := range mapping {
			if strings.HasPrefix(key, candidate) && len(key) > len(candidate) {
				r, _ := utf8.DecodeRuneInString(key[len(candidate):])
				children[r] = true
			}
		}
		for r := rune('!'); r <= utf8.MaxRune; r++ {
			if r == '*' || !utf8.ValidRune(r) || !unicode.IsPrint(r) || children[r] {
				continue
			}
			unshadowed := candidate + string(r)
			if account.IsModelSupported(unshadowed) && openCodeFreeAdmissible(account.GetMappedModel(unshadowed)) {
				return unshadowed
			}
			break // all unshadowed branches have the same effective mapping
		}
		ordered := make([]rune, 0, len(children))
		for child := range children {
			ordered = append(ordered, child)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
		for _, child := range ordered {
			if found := visit(candidate + string(child)); found != "" {
				return found
			}
		}
		return ""
	}
	return visit(prefix)
}

// Even a compromised worker must not be able to echo transport credentials
// into the admin event stream. Redact URL-encoded variants as well.
func redactOpenCodeFreeTestContent(text, token string, proxy *Proxy) string {
	secrets := []string{token}
	if proxy != nil {
		secrets = append(secrets, proxy.Username, proxy.Password)
	}
	var variants []string
	for _, secret := range secrets {
		if secret != "" {
			variants = append(variants, secret, url.QueryEscape(secret), url.PathEscape(secret))
		}
	}
	sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	for _, secret := range variants {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}
