package service

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"time"
)

// The OpenCode free tier rejects an exhausted instance with HTTP 429 and a
// body carrying FreeUsageLimitError (observed Retry-After spans ~10-20h until
// the next-day reset). Free-tier identity is the proxy egress IP, so the
// exhausted account+proxy pair must leave the scheduler until its window
// returns and the in-flight request should switch to another instance of the
// same group.
const (
	openCodeFreeQuotaCooldownFallback = time.Hour
	openCodeFreeQuotaCooldownMin      = time.Minute
	openCodeFreeQuotaCooldownMax      = 24 * time.Hour
	openCodeFreeLogSnippetMax         = 512
)

var (
	openCodeFreeQuotaBodyMarkers = [][]byte{
		[]byte("FreeUsageLimitError"),
		[]byte("Free usage limit reached"),
	}
	openCodeFreeUserinfoRe = regexp.MustCompile(`https?://[^\s"'@]+:[^\s"'@]+@`)
	openCodeFreeBearerRe   = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-]+`)
)

// isOpenCodeFreeQuotaExhausted reports whether an upstream response is the
// free-tier quota rejection rather than a transient or request-scoped error.
func isOpenCodeFreeQuotaExhausted(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests || len(body) == 0 {
		return false
	}
	for _, marker := range openCodeFreeQuotaBodyMarkers {
		if bytes.Contains(body, marker) {
			return true
		}
	}
	return false
}

// redactOpenCodeFreeLogSnippet bounds a fragment and scrubs proxy userinfo
// URLs and bearer tokens before it reaches logs; proxy/edge junk bodies can
// embed credentials.
func redactOpenCodeFreeLogSnippet(text string) string {
	text = openCodeFreeUserinfoRe.ReplaceAllString(text, "http://***:***@")
	text = openCodeFreeBearerRe.ReplaceAllString(text, "Bearer ***")
	if len(text) > openCodeFreeLogSnippetMax {
		text = text[:openCodeFreeLogSnippetMax]
	}
	return text
}

// openCodeFreeBodySnippet converts an upstream/worker error body into a
// bounded, credential-scrubbed log fragment.
func openCodeFreeBodySnippet(body []byte) string {
	if len(body) > 4096 {
		body = body[:4096]
	}
	return redactOpenCodeFreeLogSnippet(string(body))
}

// HandleOpenCodeFreeQuota429 parks an exhausted OpenCode free instance until
// its quota window returns: Retry-After when the upstream provides one,
// otherwise a bounded fallback. The reset lands in RateLimitResetAt so the
// account auto-recovers and the admin UI shows the remaining window. It
// returns the applied reset time, zero when nothing was parked.
func (s *RateLimitService) HandleOpenCodeFreeQuota429(ctx context.Context, account *Account, headers http.Header) time.Time {
	if s == nil || s.accountRepo == nil {
		return time.Time{}
	}
	return applyOpenCodeFreeQuotaCooldown(ctx, s.accountRepo, account, headers, s.notifyAccountSchedulingBlocked)
}

// applyOpenCodeFreeQuotaCooldown is shared by the gateway and the admin
// diagnostic; notify may be nil when no runtime blocker is available.
func applyOpenCodeFreeQuotaCooldown(ctx context.Context, repo AccountRepository, account *Account, headers http.Header, notify func(*Account, time.Time, string)) time.Time {
	if repo == nil || account == nil || account.ID <= 0 {
		return time.Time{}
	}
	now := time.Now()
	until := now.Add(openCodeFreeQuotaCooldownFallback)
	if parsed := parseRetryAfterResetTime(headers, now); parsed != nil {
		until = *parsed
	}
	if until.Before(now.Add(openCodeFreeQuotaCooldownMin)) {
		until = now.Add(openCodeFreeQuotaCooldownMin)
	}
	if until.After(now.Add(openCodeFreeQuotaCooldownMax)) {
		until = now.Add(openCodeFreeQuotaCooldownMax)
	}
	if notify != nil {
		notify(account, until, "429")
	}
	if err := repo.SetRateLimited(ctx, account.ID, until); err != nil {
		slog.Warn("opencode_free_quota_cooldown_failed",
			"account_id", account.ID,
			"platform", account.Platform,
			"reset_at", until,
			"error", err)
		return time.Time{}
	}
	slog.Warn("opencode_free_quota_cooldown",
		"account_id", account.ID,
		"platform", account.Platform,
		"reset_at", until,
		"reset_in", until.Sub(now).Truncate(time.Second),
		"retry_after", headers.Get("Retry-After"))
	return until
}
