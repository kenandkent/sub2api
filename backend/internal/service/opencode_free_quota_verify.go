package service

import (
	"bytes"
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
// until next-day 08:00). The cooldown cap bounds each park to twelve hours,
// but that window can still cause avoidable capacity loss when the rejection
// no longer holds. So every quota park — gateway and admin diagnostic alike —
// schedules an async verify run: one immediate probe plus, while the park still
// holds, the bounded re-probe schedule in openCodeFreeVerifyRetryDelays. Each
// probe is a minimal fresh-session request dispatched through the same worker
// transport boundary as real traffic, so a window that stopped holding is
// released hours before the capped reset instead of idling out.
//
// The probe's job is only to classify the park, never to guess a new window:
//   - upstream completes a valid, visible free answer -> the park was
//     abnormal/over-stated, so the cooldown generation captured at probe start
//     is cleared via an atomic repository CAS and the scheduler block announced
//     clear; the instance is immediately selectable again;
//   - upstream still returns the quota 429 -> the quota rejection remains
//     reproducible, so the park stays for later probes or its capped reset;
//   - anything else (transport failure, worker error, unreadable body) ->
//     inconclusive, the park stays; the cap bounds staleness either way.
//
// All writes are generation-bound: the CAS matches the exact
// rate_limited_at/rate_limit_reset_at pair read when the probe started, so a
// slow probe can never erase a newer 429, an admin clear/re-arm, or an
// unrelated update. One verify run per account runs at a time (in-flight
// guard, held across the whole re-probe schedule), and the repository marker
// interface keeps test stubs that cannot CAS-clear out of the scheduling path
// entirely.

const (
	// openCodeFreeVerifyTimeout bounds the whole probe (read row, dispatch,
	// classify, CAS) regardless of the configured worker request timeout.
	openCodeFreeVerifyTimeout = 60 * time.Second
	// openCodeFreeVerifyGrace is added on top of the bounded worker window so
	// the classification/CAS steps still fit after the dispatch deadline.
	openCodeFreeVerifyGrace = 10 * time.Second
	// openCodeFreeVerifyBodyRead bounds the prefix sniff used to recognize a
	// quota rejection. Success must replay this prefix into full validation.
	openCodeFreeVerifyBodyRead = 8192
	// Probe validation never exceeds the worker's response/event limits, even
	// if a caller supplies larger transport limits. Smaller configured limits
	// still apply.
	openCodeFreeVerifyMaxResponseBytes = 8 << 20
	openCodeFreeVerifyMaxEventBytes    = 256 << 10
)

// openCodeFreeVerifyRetryDelays is the re-probe schedule applied while a park
// keeps holding: the first probe always runs immediately, then these delays
// re-check the instance before the capped twelve-hour cooldown expires. The
// Retry-After is documented to over-state the block, so a run that ends on a
// single confirmation would leave recoverable capacity parked for hours.
// The schedule stays well inside the cap (cumulative ~10h20m with the default
// five entries) and stops early when the next probe would land at or after the
// observed reset — a fresh park schedules its own run. A package variable so
// tests can shorten it.
var openCodeFreeVerifyRetryDelays = []time.Duration{
	20 * time.Minute,
	40 * time.Minute,
	80 * time.Minute,
	160 * time.Minute,
	320 * time.Minute,
}

// openCodeFreeVerifyOutcome reports the park state after one probe, so the
// scheduling run knows whether another probe is worth dispatching.
type openCodeFreeVerifyOutcome int

const (
	// openCodeFreeVerifyStopped means this run needs no further probe: the park
	// is gone or already cleared, the repository is unusable, or the probe
	// could not even be built (a configuration error a retry would repeat).
	openCodeFreeVerifyStopped openCodeFreeVerifyOutcome = iota
	// openCodeFreeVerifyParked means the park still holds after the probe —
	// either confirmed by an upstream quota 429 or left untouched by an
	// inconclusive result — so a later re-probe is still worth dispatching.
	openCodeFreeVerifyParked
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

// openCodeFreeQuotaVerifyRun serializes the hand-off between an ending run
// and another quota park. A coalesced request marks pending, so even a park
// arriving after the last row read or successful CAS receives a later probe.
// closed distinguishes a run already removed from the guard from a live run
// that can accept a wakeup.
type openCodeFreeQuotaVerifyRun struct {
	mu      sync.Mutex
	pending bool
	closed  bool
}

// openCodeFreeQuotaVerifyInFlight guards one verify run per account across its
// immediate-plus-reprobe schedule and remembers requests coalesced into it.
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
	accountID := account.ID
	for {
		run := &openCodeFreeQuotaVerifyRun{}
		current, loaded := openCodeFreeQuotaVerifyInFlight.LoadOrStore(accountID, run)
		if loaded {
			active := current.(*openCodeFreeQuotaVerifyRun)
			active.mu.Lock()
			if active.closed {
				// A caller may have loaded the old entry immediately before its
				// atomic close/delete. Retry so this park starts a fresh run.
				active.mu.Unlock()
				continue
			}
			active.pending = true
			active.mu.Unlock()
			return
		}
		go func() {
			defer func() {
				// Preserve panic cleanup without allowing an old run's defer to
				// remove a replacement installed after the terminal hand-off.
				run.mu.Lock()
				run.closed = true
				openCodeFreeQuotaVerifyInFlight.CompareAndDelete(accountID, run)
				run.mu.Unlock()
				if rec := recover(); rec != nil {
					slog.Warn("opencode_free_quota_verify_panicked",
						"account_id", accountID,
						"panic", fmt.Sprintf("%v", rec))
				}
			}()
			for {
				s.runOpenCodeFreeQuotaVerify(accountID, model, cfg)
				run.mu.Lock()
				if run.pending {
					run.pending = false
					run.mu.Unlock()
					continue
				}
				// Closing and deleting under the same mutex prevents a caller
				// from acknowledging a wakeup that this run will never consume.
				run.closed = true
				openCodeFreeQuotaVerifyInFlight.CompareAndDelete(accountID, run)
				run.mu.Unlock()
				return
			}
		}()
		return
	}
}

// runOpenCodeFreeQuotaVerify is the scheduling run behind the in-flight guard:
// it probes immediately, then re-probes on openCodeFreeVerifyRetryDelays for as
// long as the park still holds. It stops as soon as the park clears, the probe
// cannot be built, the schedule is exhausted, or the next delay would cross the
// observed reset — at that point the park's own expiry hands the instance back
// to the scheduler, which starts a fresh run if it 429s again.
func (s *RateLimitService) runOpenCodeFreeQuotaVerify(accountID int64, model string, cfg config.OpenCodeFreeBunConfig) {
	for attempt := 0; ; attempt++ {
		outcome, resetAt := s.verifyOpenCodeFreeQuota(accountID, model, cfg)
		if outcome != openCodeFreeVerifyParked {
			return
		}
		if attempt >= len(openCodeFreeVerifyRetryDelays) {
			slog.Info("opencode_free_quota_verify_schedule_exhausted",
				"account_id", accountID,
				"probes", attempt+1)
			return
		}
		delay := openCodeFreeVerifyRetryDelays[attempt]
		if delay <= 0 {
			return
		}
		if resetAt != nil && !time.Now().Add(delay).Before(*resetAt) {
			// The re-probe would only run once the cooldown has effectively
			// expired; the scheduler handles that transition already, so stop
			// instead of holding the in-flight guard for no benefit.
			slog.Info("opencode_free_quota_verify_schedule_deferred_to_reset",
				"account_id", accountID,
				"probes", attempt+1,
				"reset_at", resetAt.UTC(),
				"delay", delay)
			return
		}
		time.Sleep(delay)
	}
}

// verifyOpenCodeFreeQuota runs a single probe synchronously and reports whether
// the park still holds (openCodeFreeVerifyParked) together with the reset
// observed for it, which bounds the caller's re-probe schedule. It re-reads the
// authoritative row (capturing the exact cooldown generation to CAS against),
// dispatches a minimal fresh-session request through the worker transport, and
// applies the classification rules above.
func (s *RateLimitService) verifyOpenCodeFreeQuota(accountID int64, model string, cfg config.OpenCodeFreeBunConfig) (openCodeFreeVerifyOutcome, *time.Time) {
	repo, ok := s.accountRepo.(openCodeFreeQuotaVerifyRepo)
	if !ok {
		return openCodeFreeVerifyStopped, nil
	}
	probeWindow := time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	if probeWindow <= 0 || probeWindow > openCodeFreeVerifyTimeout {
		probeWindow = openCodeFreeVerifyTimeout
	}
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), probeWindow+openCodeFreeVerifyGrace)
	defer cancel()

	row, err := repo.GetByID(bgCtx, accountID)
	if err != nil || row == nil {
		// A row that cannot be read now may read fine at the next delay; the
		// schedule bounds how often that can happen.
		slog.Warn("opencode_free_quota_verify_load_failed",
			"account_id", accountID,
			"error", err)
		return openCodeFreeVerifyParked, nil
	}
	if row.RateLimitedAt == nil || row.RateLimitResetAt == nil || !row.RateLimitResetAt.After(time.Now()) {
		// Nothing parked anymore (expired or cleared before the probe ran).
		return openCodeFreeVerifyStopped, nil
	}
	observedLimitedAt, observedResetAt := *row.RateLimitedAt, *row.RateLimitResetAt

	reqNorm, err := normalizeOpenCodeFreeRequest(openCodeFreeProbeBody(model), model)
	if err != nil {
		slog.Warn("opencode_free_quota_verify_probe_invalid",
			"account_id", accountID,
			"model", model,
			"error", err)
		return openCodeFreeVerifyStopped, nil
	}
	session, err := freeSessionID()
	if err != nil {
		slog.Warn("opencode_free_quota_verify_session_failed",
			"account_id", accountID,
			"error", err)
		return openCodeFreeVerifyStopped, nil
	}
	proxy, err := openCodeFreeProxy(row)
	if err != nil {
		slog.Warn("opencode_free_quota_verify_proxy_invalid",
			"account_id", accountID,
			"error", err)
		return openCodeFreeVerifyStopped, nil
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
		return openCodeFreeVerifyParked, &observedResetAt
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("X-Sub2api-Transport-Result") != "upstream" || resp.Header.Get("X-Sub2api-Dispatched") != "true" {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "worker",
			"worker_status", resp.StatusCode,
			"transport_result", resp.Header.Get("X-Sub2api-Transport-Result"))
		return openCodeFreeVerifyParked, &observedResetAt
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, openCodeFreeVerifyBodyRead))
	if readErr != nil {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "read",
			"error", readErr)
		return openCodeFreeVerifyParked, &observedResetAt
	}
	if isOpenCodeFreeQuotaExhausted(resp.StatusCode, data) {
		// The limit still holds upstream: keep the (capped) park untouched.
		slog.Info("opencode_free_quota_verify_confirmed",
			"account_id", accountID,
			"model", model,
			"upstream_status", resp.StatusCode,
			"reset_at", observedResetAt.UTC(),
			"reset_in", time.Until(observedResetAt).Truncate(time.Second))
		return openCodeFreeVerifyParked, &observedResetAt
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "classify",
			"upstream_status", resp.StatusCode,
			"content_type", resp.Header.Get("Content-Type"),
			"retry_after", resp.Header.Get("Retry-After"))
		return openCodeFreeVerifyParked, &observedResetAt
	}
	maxResponse := cfg.MaxResponseBytes
	if maxResponse <= 0 || maxResponse > openCodeFreeVerifyMaxResponseBytes {
		maxResponse = openCodeFreeVerifyMaxResponseBytes
	}
	maxEvent := cfg.MaxEventBytes
	if maxEvent <= 0 || maxEvent > openCodeFreeVerifyMaxEventBytes {
		maxEvent = openCodeFreeVerifyMaxEventBytes
	}
	// HTTP200 establishes only transport success. Replay the sniffed prefix
	// and read through EOF under the original dispatch context, requiring the
	// same finish/DONE/free-usage contract as a real forwarding request. The
	// text-only probe must also have visible answer text; reasoning alone
	// cannot establish that the instance has recovered.
	parsed, parseErr := parseOpenCodeFreeSSE(probeCtx,
		io.MultiReader(bytes.NewReader(data), resp.Body), nil,
		maxResponse, maxEvent, model, &openCodeFreeSSEOptions{
			ToolNames:     reqNorm.ToolNames,
			ToolChoice:    reqNorm.ToolChoice,
			InjectedTools: reqNorm.InjectedTools,
			QuietErrors:   true,
			ExpectedModel: model,
		})
	if parseErr != nil || parsed == nil || parsed.BillingSource != "free" || !openCodeFreeHasVisibleAnswer(parsed.Text, 0) {
		slog.Info("opencode_free_quota_verify_inconclusive",
			"account_id", accountID,
			"model", model,
			"stage", "validate",
			"error", parseErr)
		return openCodeFreeVerifyParked, &observedResetAt
	}
	cleared, err := repo.ClearRateLimitIfObservedOpenCodeFree(bgCtx, accountID, observedLimitedAt, observedResetAt)
	if err != nil {
		// The park is untouched on a failed CAS, so a later probe is still
		// worth dispatching.
		slog.Warn("opencode_free_quota_verify_clear_failed",
			"account_id", accountID,
			"error", err)
		return openCodeFreeVerifyParked, &observedResetAt
	}
	if !cleared {
		// The generation moved on (newer 429, admin clear/re-arm): conservatively
		// leave whatever is there now alone. No reset bound is reported — the
		// next probe re-reads the authoritative row and adopts its window.
		slog.Debug("opencode_free_quota_verify_clear_skipped_stale",
			"account_id", accountID)
		return openCodeFreeVerifyParked, nil
	}
	s.notifyAccountSchedulingBlockCleared(accountID)
	slog.Info("opencode_free_quota_verify_restored",
		"account_id", accountID,
		"model", model,
		"upstream_status", resp.StatusCode,
		"cleared_reset_at", observedResetAt.UTC())
	return openCodeFreeVerifyStopped, nil
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
