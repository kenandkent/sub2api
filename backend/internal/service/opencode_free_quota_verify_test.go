package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// quotaVerifyRepoStub implements the verify-path marker interface (row read +
// generation-bound CAS clear) on top of a nil AccountRepository; only the two
// methods the probe actually calls are defined.
type quotaVerifyRepoStub struct {
	AccountRepository

	row             *Account
	getErr          error
	clearCalls      int
	observedLimited time.Time
	observedReset   time.Time
	cleared         bool
	clearErr        error
}

func (s *quotaVerifyRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.row == nil {
		return nil, errors.New("row not found")
	}
	return s.row, nil
}

func (s *quotaVerifyRepoStub) ClearRateLimitIfObservedOpenCodeFree(ctx context.Context, id int64, limitedAt, resetAt time.Time) (bool, error) {
	s.clearCalls++
	s.observedLimited = limitedAt
	s.observedReset = resetAt
	return s.cleared, s.clearErr
}

func quotaVerifyTestConfig(endpoint string) config.OpenCodeFreeBunConfig {
	return config.OpenCodeFreeBunConfig{
		Enabled:               true,
		WorkerEndpoint:        endpoint,
		Token:                 "12345678901234567890123456789012",
		RequestTimeoutSeconds: 5,
		MaxResponseBytes:      1 << 20,
		MaxEventBytes:         1 << 16,
	}
}

func quotaVerifyParkedRow(limited, reset time.Time) *Account {
	return &Account{
		ID:               1,
		Platform:         PlatformOpenCodeGo,
		Type:             AccountTypeAPIKey,
		RateLimitedAt:    &limited,
		RateLimitResetAt: &reset,
	}
}

// A park whose upstream already serves 200 again (the observed over-stated
// retry-after case) must be cleared via the exact generation captured at probe
// start so the instance rejoins the scheduler immediately.
func TestOpenCodeFreeQuotaVerifyClearsWhenUpstreamRecovers(t *testing.T) {
	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer worker.Close()

	limited := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	reset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Millisecond)
	repo := &quotaVerifyRepoStub{row: quotaVerifyParkedRow(limited, reset), cleared: true}
	svc := &RateLimitService{accountRepo: repo}

	svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, int32(1), hits.Load(), "the probe must dispatch one worker request")
	require.Equal(t, 1, repo.clearCalls, "a recovered upstream must clear the cooldown")
	require.True(t, repo.observedLimited.Equal(limited), "the CAS must carry the observed rate_limited_at")
	require.True(t, repo.observedReset.Equal(reset), "the CAS must carry the observed rate_limit_reset_at")
}

// A still-confirmed upstream quota 429 must leave the park untouched, no
// matter what the CAS would have done.
func TestOpenCodeFreeQuotaVerifyKeepsParkedOnQuota429(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Retry-After", "50000")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"FreeUsageLimitError","message":"Free usage limit reached"}}`))
	}))
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true, // even a ready CAS must not fire on a confirmed quota wall
	}
	svc := &RateLimitService{accountRepo: repo}

	svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, 0, repo.clearCalls, "a confirmed quota 429 must keep the cooldown")
}

// Worker-side failures carry no upstream transport evidence; the probe is
// inconclusive and must never clear on them.
func TestOpenCodeFreeQuotaVerifyKeepsParkedOnWorkerFailure(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"worker failed"}}`))
	}))
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}

	svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, 0, repo.clearCalls, "an inconclusive probe must keep the cooldown")
}

// A non-quota upstream error with valid transport headers is just as
// inconclusive: only 200 proves recovery.
func TestOpenCodeFreeQuotaVerifyKeepsParkedOnNonQuotaError(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}

	svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, 0, repo.clearCalls, "a non-200 non-quota result must keep the cooldown")
}

// When the cooldown expired or was already cleared between park and probe,
// there is nothing to verify: no dispatch, no write.
func TestOpenCodeFreeQuotaVerifySkipsWhenCooldownAlreadyGone(t *testing.T) {
	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}

	svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, int32(0), hits.Load(), "an expired cooldown must not be probed")
	require.Equal(t, 0, repo.clearCalls, "an expired cooldown must not be cleared again")
}

// Scheduling requires the concrete-repository marker; a stub that only embeds
// AccountRepository must skip before touching the in-flight guard or spawning
// a goroutine.
func TestScheduleOpenCodeFreeQuotaVerifyNoopsWithoutRecoveryRepo(t *testing.T) {
	svc := &RateLimitService{accountRepo: &quotaCooldownRepoStub{}}
	svc.ScheduleOpenCodeFreeQuotaVerify(&Account{ID: 7, Platform: PlatformOpenCodeGo}, "big-pickle", quotaVerifyTestConfig("http://127.0.0.1:9/v1/transport/chat"))
	_, loaded := openCodeFreeQuotaVerifyInFlight.Load(int64(7))
	require.False(t, loaded, "a repo without the CAS clear must never schedule a probe")
}

// The schedule path runs the probe asynchronously and clears on recovery.
func TestScheduleOpenCodeFreeQuotaVerifyClearsAsynchronously(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 42, Platform: PlatformOpenCodeGo}

	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Eventually(t, func() bool { return repo.clearCalls == 1 }, 3*time.Second, 10*time.Millisecond)
	_, loaded := openCodeFreeQuotaVerifyInFlight.Load(int64(42))
	require.False(t, loaded, "the in-flight guard must release after the probe")
}
