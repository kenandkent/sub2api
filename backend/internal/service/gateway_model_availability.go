package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ModelAvailabilityDiagnosis describes whether the requested model can be
// served by any persistently eligible account in the group (active with its
// schedulable setting enabled), ignoring transient state such as rate limits,
// overload, temporary unschedulability, and runtime blocks. Handlers use this
// on the "no available accounts" error path to distinguish 404
// model_not_found from 503 service_unavailable.
type ModelAvailabilityDiagnosis struct {
	// HasAccountsInPool is true if the group has at least one persistently
	// eligible account on the queried platform (or, for Anthropic/Gemini, on
	// the platform plus mixed-scheduled Antigravity accounts).
	HasAccountsInPool bool
	// HasModelSupport is true if at least one account's model mapping admits
	// the requested model.
	HasModelSupport bool
	// HasRetiredSubscriptionModel records a ChatGPT upstream blocked by model
	// retirement, so an unsupported request can include migration guidance.
	HasRetiredSubscriptionModel bool
	// ModelCapableAccounts counts persistently eligible candidates whose model
	// mapping admits the requested model.
	ModelCapableAccounts int
	// ModelCapableRateLimited counts model-capable candidates currently blocked
	// by an account-level or model-level rate-limit window. When it equals
	// ModelCapableAccounts on a non-empty pool, selection failed purely because
	// of transient rate limits and handlers may answer 429 instead of 503.
	ModelCapableRateLimited int
	// EarliestRateLimitResetAt is the earliest time at which any rate-limited
	// model-capable candidate becomes schedulable again; zero when unknown.
	// Handlers surface it as the Retry-After response header.
	EarliestRateLimitResetAt time.Time
}

// accountModelRateLimitState reports whether an otherwise persistently
// eligible account is currently blocked by a rate-limit window for the
// requested model, and if so the earliest time it may become schedulable
// again. Eligibility requires BOTH the account-level window
// (RateLimitResetAt) and the model-level window to have ended, so the later
// boundary wins. The Antigravity credit-backed exception mirrors
// IsSchedulableForModelWithContext: a model-level window does not block when
// overages credits are still available.
//
// Deliberately narrower than IsSchedulableForModelWithContext: expiry,
// overload, temporary-unschedulability and quota exhaustion are not rate
// limits, so a pool blocked by those states must keep the 503 classification
// instead of being misreported as 429.
func accountModelRateLimitState(ctx context.Context, acc *Account, requestedModel string, now time.Time) (bool, time.Time) {
	if acc == nil {
		return false, time.Time{}
	}
	limited := false
	var eligibleAt time.Time
	if acc.RateLimitResetAt != nil && now.Before(*acc.RateLimitResetAt) {
		limited = true
		eligibleAt = *acc.RateLimitResetAt
	}
	creditBacked := acc.Platform == PlatformAntigravity && acc.IsOveragesEnabled() && !acc.isCreditsExhausted()
	if acc.isModelRateLimitedWithContext(ctx, requestedModel) && !creditBacked {
		limited = true
		modelEligibleAt := now
		if remaining := acc.GetRateLimitRemainingTimeWithContext(ctx, requestedModel); remaining > 0 {
			modelEligibleAt = now.Add(remaining)
		}
		if eligibleAt.IsZero() || modelEligibleAt.After(eligibleAt) {
			eligibleAt = modelEligibleAt
		}
	}
	return limited, eligibleAt
}

// observeModelRateLimit records one rate-limited model-capable candidate on
// diag, tracking the earliest reset across the pool.
func observeModelRateLimit(diag *ModelAvailabilityDiagnosis, limited bool, resetAt time.Time) {
	if !limited {
		return
	}
	diag.ModelCapableRateLimited++
	if resetAt.IsZero() {
		return
	}
	if diag.EarliestRateLimitResetAt.IsZero() || resetAt.Before(diag.EarliestRateLimitResetAt) {
		diag.EarliestRateLimitResetAt = resetAt
	}
}

// ModelAvailabilityDiagnoser is implemented by gateway services that can
// report whether the requested model is configured to be served by any
// account. Both *GatewayService and *OpenAIGatewayService implement this so
// handlers in either package can share a single classifier.
type ModelAvailabilityDiagnoser interface {
	DiagnoseModelAvailabilityForPlatform(
		ctx context.Context,
		groupID *int64,
		requestedModel string,
		platform string,
	) ModelAvailabilityDiagnosis
}

// DiagnoseModelAvailabilityForPlatform inspects accounts enabled for scheduling
// by persistent configuration and returns whether the requested model is
// configured to be served by any of them. The dedicated repository query
// bypasses scheduler snapshots and deliberately ignores transient rate-limit,
// overload, temporary-unschedulable, expiry, quota, and runtime-block state.
//
// Safe to call on the error path: returns {true,true} on any internal failure
// or when the inputs preclude meaningful diagnosis (empty model, etc.), so
// callers stay on the 503 fallback branch.
func (s *GatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		// No model specified — cannot decide model_not_found. Caller falls back to 503.
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	if strings.TrimSpace(platform) == "" {
		// Without a platform we cannot scope the lookup; bail out to the
		// 503 branch rather than make an unscoped scan.
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	if s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	useMixed := platform == PlatformAnthropic || platform == PlatformGemini
	platforms := []string{platform}
	if useMixed {
		platforms = append(platforms, PlatformAntigravity)
	}

	queryGroupID := groupID
	includeGrouped := false
	if useMixed {
		// Preserve the generic scheduler's scope rules: an explicit group wins
		// for mixed scheduling, while group-less simple mode scans all accounts.
		if groupID == nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
			includeGrouped = true
		}
	} else if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		queryGroupID = nil
		includeGrouped = true
	}

	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, queryGroupID, platforms, includeGrouped)
	if err != nil {
		// Conservative fallback: pretend everything is fine so the caller
		// returns 503 (we don't want to flip to 404 just because a lookup
		// hiccup'd).
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
	now := time.Now()
	for i := range accounts {
		if useMixed && accounts[i].Platform == PlatformAntigravity && !accounts[i].IsMixedSchedulingEnabled() {
			continue
		}
		diag.HasAccountsInPool = true
		if !s.isModelSupportedByAccountWithContext(ctx, &accounts[i], requestedModel) {
			continue
		}
		diag.HasModelSupport = true
		diag.ModelCapableAccounts++
		limited, resetAt := accountModelRateLimitState(ctx, &accounts[i], requestedModel, now)
		observeModelRateLimit(&diag, limited, resetAt)
	}
	return diag
}
