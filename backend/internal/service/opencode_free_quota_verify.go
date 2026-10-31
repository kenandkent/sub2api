package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// The upstream free tier rejects an exhausted instance with 429 plus a
// Retry-After that over-states the real block (observed 2026-10-31: every
// parked instance served 200 again within hours while the row stayed parked
// until next-day 08:00). The cooldown cap already bounds the damage to one
// hour, but a capped hour is still an hour of avoidable capacity loss when the
// rejection was abnormal rather than a true quota wall. So every quota park
// schedules exactly one async verify probe: a minimal fresh-session request
// dispatched through the same worker transport boundary as real traffic.
//
// The probe's job is only to classify the park, never to guess a new window:
//   - upstream serves 200 again -> the park was abnormal/over-stated, so the
//     cooldown generation captured from the row at probe start is cleared via
//     an atomic repository CAS and the runtime scheduler block is announced
//     clear; the instance is immediately selectable again;
//   - upstream still returns the quota 429 -> the limit is real, the park
//     stays until its (already capped) reset;
//   - anything else (transport failure, worker error, unreadable body) ->
//     inconclusive, the park stays; the cap bounds staleness either way.
//
// All writes are generation-bound: the CAS matches the exact
// rate_limited_at/rate_limit_reset_at pair read when the probe started, so a
// slow probe can never erase a newer 429, an admin clear/re-arm, or an
// unrelated update. One probe per account runs at a time (in-flight guard),
// and the repository marker interface keeps test stubs that cannot CAS-clear
// out of the scheduling path entirely.

const (
	// openCodeFreeVerifyTimeout bounds the whole probe (read row, dispatch,
	// classify, CAS) regardless of the configured worker request timeout.
	openCodeFreeVerifyTimeout = 60 * time.Second
	// openCodeFreeVerifyGrace is added on top of the bounded worker window so
	// the classification/CAS steps still fit after the dispatch deadline.
	openCodeFreeVerifyGrace = 10 * time.Second
	// openCodeFreeVerifyBodyRead bounds how much of the probe response is
	// read; quota bodies are small, so a few KiB suffices for classification.
	openCodeFreeVerifyBodyRead = 8192
)

// openCodeFreeQuotaVerifyRepo is the concrete-repository surface the verify
// path needs: the authoritative row read plus the generation-bound CAS clear.
// It is intentionally a structural marker beyond AccountRepository — test
// stubs that only embed AccountRepository never satisfy it, so they skip
// scheduling instead of hitting a nil embedded method.
type openCodeFreeQuotaVerifyRepo interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
	ClearRateLimitIfObservedOpenCodeFree(ctx context.Context, id int64, observedLimitedAt, observedResetAt time.Time) (bool, error)
}

// openCodeFreeQuotaVerifyInFlight guards one verify probe per account; a
// second quota park while a probe runs simply skips (the current generation
// stays parked and the next park schedules a fresh probe).
var openCodeFreeQuotaVerifyInFlight sync.Map

// ScheduleOpenCodeFreeQuotaVerify launches the async post-park verify for an
// OpenCode free instance that HandleOpenCodeFreeQuota429 just parked. model is
// the mapped upstream model of the request that hit the 429 (the probe must
// exercise the same profile/admissibility path), cfg the validated OpenCode
// free transport config from the forwarding request. It never blocks the
// caller and never runs when the repository cannot CAS-clear.
func (s *RateLimitService) ScheduleOpenCodeFreeQuotaVerify(account *Account, model string, cfg config.OpenCodeFreeBunConfig) {
	if s == nil || account == nil || account.ID <= 0 || model == "" || !cfg.Enabled {
		return
	}
	if _, ok := s.accountRepo.(openCodeFreeQuotaVerifyRepo); !ok {
		return
	}
	if _, loaded := openCodeFreeQuotaVerifyInFlight.LoadOrStore(account.ID, struct{}{}); loaded {
		return
	}
	accountID := account.ID
	go func() {
		defer openCodeFreeQuotaVerifyInFlight.Delete(accountID)
		defer func() {
			if rec := recover(); rec != nil {
				slog.Warn("opencode_free_quota_verify_panicked",
					"account_id", accountID,
					"panic", fmt.Sprintf("%v", rec))
			}
		}()
		s.verifyOpenCodeFreeQuota(accountID, model, cfg)
	}()
}

// verifyOpenCodeFreeQuota runs the single probe synchronously. It re-reads the
// authoritative row (capturing the exact cooldown generation to CAS against),
// dispatches a minimal fresh-session request through the worker transport, and
// applies the classification rules above.
func (s *RateLimitService) verifyOpenCodeFreeQuota(accountID int64, model string, cfg config.OpenCodeFreeBunConfig) {
	repo, ok := s.accountRepo.(openCodeFreeQuotaVerifyRepo)
	if !ok {
		return
	}
	probeWindow := time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	if probeWindow <= 0 || probeWindow > openCodeFreeVerifyTimeout {
		probeWindow = openCodeFreeVerifyTimeout
	}
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), probeWindow+openCodeFreeVerifyGrace)
	defer cancel()

	row, err := repo.GetByID(bgCtx, accountID)
	if err != nil || row == nil {
		slog.Warn("opencode_free_quota_verify_load_failed",
			"account_id", accountID,
			"error", err)
		return
	}
	if row.RateLimitedAt == nil || row.RateLimitResetAt == nil || !row.RateLimitResetAt.After(time.Now()) {
		// Nothing parked anymore (expired or cleared before the probe ran).
		return
	}
	observedLimitedAt, observedResetAt := *row.RateLimitedAt, *row.RateLimitResetAt

	reqNorm, err := normalizeOpenCodeFreeRequest(openCodeFreeProbeBody(model), model)
	if err != nil {
		slog.Warn("opencode_free_quota_verify_probe_invalid",
			"account_id", accountID,
			"model", model,
			"error", err)
		return
	}
	session, err := freeSessionID()
	if err != nil {
		slog.Warn("opencode_free_quota_verify_session_failed",
			"account_id", accountID,
			"error", err)
		return
	}
	proxy, err := openCodeFreeProxy(row)
	if err != nil {
		slog.Warn("opencode_free_quota_verify_proxy_invalid",
			"account_id", accountID,
			"error", err)
		return
	}
	probeCtx, probeCancel := context.WithTimeout(bgCtx, probeWindow)
	defer probeCancel()
	deadline, _ := probeCtx.Deadline()
	env := freeWorkerEnvelope{
		Version:        1,
		RequestID:      fmt.Sprintf("opencode-free-verify-%d-%d", accountID, time.Now().UnixNano()),
		ProfileID:      reqNorm.Profile.ID,
		ProfileSHA256:  reqNorm.Profile.SHA256,
		Target:         "console",
		AccountID:      accountID,
		SessionID:      session,
		DeadlineUnixMS: deadline.UnixMilli(),
		Proxy:          proxy,
		Body:           reqNorm.Body,
	}
	resp, err := dispatchOpenCodeFree(probeCtx, cfg, env)
	if err != nil {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "dispatch",
			"error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("X-Sub2api-Transport-Result") != "upstream" || resp.Header.Get("X-Sub2api-Dispatched") != "true" {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "worker",
			"worker_status", resp.StatusCode,
			"transport_result", resp.Header.Get("X-Sub2api-Transport-Result"))
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, openCodeFreeVerifyBodyRead))
	if readErr != nil {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "read",
			"error", readErr)
		return
	}
	if isOpenCodeFreeQuotaExhausted(resp.StatusCode, data) {
		// The limit still holds upstream: keep the (capped) park untouched.
		slog.Info("opencode_free_quota_verify_confirmed",
			"account_id", accountID,
			"model", model,
			"upstream_status", resp.StatusCode,
			"reset_at", observedResetAt.UTC(),
			"reset_in", time.Until(observedResetAt).Truncate(time.Second))
		return
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "classify",
			"upstream_status", resp.StatusCode,
			"content_type", resp.Header.Get("Content-Type"),
			"retry_after", resp.Header.Get("Retry-After"))
		return
	}
	cleared, err := repo.ClearRateLimitIfObservedOpenCodeFree(bgCtx, accountID, observedLimitedAt, observedResetAt)
	if err != nil {
		slog.Warn("opencode_free_quota_verify_clear_failed",
			"account_id", accountID,
			"error", err)
		return
	}
	if !cleared {
		// The generation moved on (newer 429, admin clear/re-arm): conservatively
		// leave whatever is there now alone.
		slog.Debug("opencode_free_quota_verify_clear_skipped_stale",
			"account_id", accountID)
		return
	}
	s.notifyAccountSchedulingBlockCleared(accountID)
	slog.Info("opencode_free_quota_verify_restored",
		"account_id", accountID,
		"model", model,
		"upstream_status", resp.StatusCode,
		"cleared_reset_at", observedResetAt.UTC())
}

// openCodeFreeProbeBody builds the smallest request the text profile admits: a
// one-turn chat body with the mapped model, mirroring the proven gateway test
// shape so validation follows the exact production admissibility path.
func openCodeFreeProbeBody(mapped string) []byte {
	body, _ := json.Marshal(map[string]any{
		"model": mapped,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with the single word ok."},
		},
	})
	return body
}
