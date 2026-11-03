package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	clearCalls      atomic.Int32
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
	s.clearCalls.Add(1)
	s.observedLimited = limitedAt
	s.observedReset = resetAt
	if s.cleared && s.clearErr == nil {
		row := *s.row
		row.RateLimitedAt, row.RateLimitResetAt = nil, nil
		s.row = &row
	}
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

// quotaVerifyValidAnswerSSE is a complete literal answer, not merely an HTTP
// success or a terminal marker. Usage and finish must both precede DONE.
const quotaVerifyValidAnswerSSE = "data: {\"id\":\"probe\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"probe\",\"model\":\"big-pickle\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\n" +
	"data: [DONE]\n\n"

// These bodies used to clear the cooldown solely because the worker supplied
// HTTP200 and text/event-stream. A successful transport cannot establish that
// a free instance can answer until the entire response has been validated.
func TestOpenCodeFreeQuotaVerifyKeepsParkedUnlessCompleteFreeVisibleAnswer(t *testing.T) {
	crlf := strings.ReplaceAll(quotaVerifyValidAnswerSSE, "\n", "\r\n")
	cases := []struct {
		name          string
		body          string
		responseLimit int64
		eventLimit    int
	}{
		{name: "empty"},
		{name: "done_only", body: "data: [DONE]\n\n"},
		{name: "reasoning_only", body: strings.Replace(quotaVerifyValidAnswerSSE, `"content":"ok"`, `"reasoning_content":"checking"`, 1)},
		{name: "whitespace_only", body: strings.Replace(quotaVerifyValidAnswerSSE, `"content":"ok"`, `"content":" \n\t "`, 1)},
		{name: "no_done", body: strings.TrimSuffix(quotaVerifyValidAnswerSSE, "data: [DONE]\n\n")},
		{name: "no_finish", body: strings.Replace(quotaVerifyValidAnswerSSE, `"finish_reason":"stop"`, `"finish_reason":null`, 1)},
		{name: "no_usage", body: "data: {\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{name: "paid", body: strings.Replace(quotaVerifyValidAnswerSSE, `"billing_source":"free"`, `"billing_source":"paid"`, 1)},
		{name: "missing_billing", body: strings.Replace(quotaVerifyValidAnswerSSE, `,"billing_source":"free"`, "", 1)},
		{name: "malformed", body: "data: {broken}\n\n"},
		{name: "truncated_event", body: strings.TrimSuffix(quotaVerifyValidAnswerSSE, "\n\n")},
		{name: "error", body: "data: {\"error\":{\"message\":\"failed\"}}\n\n"},
		{name: "wrong_model", body: strings.ReplaceAll(quotaVerifyValidAnswerSSE, "big-pickle", "different-model")},
		{name: "wrong_model_usage", body: "data: {\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"model\":\"different-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"billing_source\":\"free\"}}\n\ndata: [DONE]\n\n"},
		{name: "crlf_event_oversize", body: crlf, eventLimit: len(strings.SplitN(crlf, "\r\n", 2)[0]) + 1},
		{name: "crlf_oversize_hides_invalid_tail", body: crlf + "data: {broken}\r\n\r\n", responseLimit: int64(len(crlf) - 1)},
		{name: "event_oversize", body: quotaVerifyValidAnswerSSE, eventLimit: 64},
		{name: "response_oversize", body: strings.Repeat(": padding padding padding padding\n\n", 80) + quotaVerifyValidAnswerSSE, responseLimit: 1024},
		{name: "hard_event_ceiling", body: strings.Replace(quotaVerifyValidAnswerSSE, `"content":"ok"`, `"content":"`+strings.Repeat("x", 300<<10)+`"`, 1), eventLimit: 1 << 20},
		{name: "hard_response_ceiling", body: strings.Repeat(": padding padding padding padding\n\n", 250000) + quotaVerifyValidAnswerSSE, responseLimit: 16 << 20},
		{name: "invalid_after_sniff_prefix", body: strings.Repeat(": padding padding padding padding\n\n", 260) + quotaVerifyValidAnswerSSE + "data: {broken}\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Sub2api-Transport-Result", "upstream")
				w.Header().Set("X-Sub2api-Dispatched", "true")
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer worker.Close()
			repo := &quotaVerifyRepoStub{row: quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(time.Hour)), cleared: true}
			svc := &RateLimitService{accountRepo: repo}
			cfg := quotaVerifyTestConfig(worker.URL)
			if tc.responseLimit > 0 {
				cfg.MaxResponseBytes = tc.responseLimit
			}
			if tc.eventLimit > 0 {
				cfg.MaxEventBytes = tc.eventLimit
			}

			outcome, _ := svc.verifyOpenCodeFreeQuota(1, "big-pickle", cfg)

			require.Equal(t, int32(0), repo.clearCalls.Load(), "an unusable answer must never attempt the cooldown CAS")
			require.Equal(t, openCodeFreeVerifyParked, outcome, "a later scheduled probe must remain possible")
		})
	}
}

// Valid answers are checked beyond the quota sniff prefix and need not match
// the prompt byte-for-byte: meaningful visible text proves instance recovery.
func TestOpenCodeFreeQuotaVerifyClearsCompleteAnswerBeyondSniffPrefix(t *testing.T) {
	body := strings.Repeat(": keepalive padding\n\n", 420) + strings.Replace(quotaVerifyValidAnswerSSE, `"content":"ok"`, `"content":"OK."`, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	defer worker.Close()
	repo := &quotaVerifyRepoStub{row: quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(time.Hour)), cleared: true}
	svc := &RateLimitService{accountRepo: repo}

	outcome, _ := svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, int32(1), repo.clearCalls.Load(), "the sniff prefix must be replayed into full stream validation")
	require.Equal(t, openCodeFreeVerifyStopped, outcome)
}

// A park whose upstream completes a usable free answer (the observed over-stated
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
		_, _ = w.Write([]byte(quotaVerifyValidAnswerSSE))
	}))
	defer worker.Close()

	limited := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	reset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Millisecond)
	repo := &quotaVerifyRepoStub{row: quotaVerifyParkedRow(limited, reset), cleared: true}
	svc := &RateLimitService{accountRepo: repo}

	svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, int32(1), hits.Load(), "the probe must dispatch one worker request")
	require.Equal(t, int32(1), repo.clearCalls.Load(), "a recovered upstream must clear the cooldown")
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

	require.Equal(t, int32(0), repo.clearCalls.Load(), "a confirmed quota 429 must keep the cooldown")
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

	require.Equal(t, int32(0), repo.clearCalls.Load(), "an inconclusive probe must keep the cooldown")
}

// A non-quota upstream error with valid transport headers is just as
// inconclusive: a complete free answer is required to prove recovery.
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

	require.Equal(t, int32(0), repo.clearCalls.Load(), "a non-200 non-quota result must keep the cooldown")
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
	require.Equal(t, int32(0), repo.clearCalls.Load(), "an expired cooldown must not be cleared again")
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
		_, _ = w.Write([]byte(quotaVerifyValidAnswerSSE))
	}))
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 42, Platform: PlatformOpenCodeGo}

	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Eventually(t, func() bool {
		_, loaded := openCodeFreeQuotaVerifyInFlight.Load(int64(42))
		return !loaded
	}, 3*time.Second, 10*time.Millisecond, "the in-flight guard must release after the probe")
	require.Equal(t, int32(1), repo.clearCalls.Load(), "the completed probe must clear the cooldown")
}

// quotaVerifyConfirmedWorker always answers with the quota wall, so a run only
// stops through its own schedule.
func quotaVerifyConfirmedWorker(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Retry-After", "50000")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"FreeUsageLimitError","message":"Free usage limit reached"}}`))
	}))
}

func stubOpenCodeFreeVerifyRetryDelays(t *testing.T, delays ...time.Duration) {
	t.Helper()
	previous := openCodeFreeVerifyRetryDelays
	openCodeFreeVerifyRetryDelays = delays
	t.Cleanup(func() { openCodeFreeVerifyRetryDelays = previous })
}

// A park whose upstream keeps confirming the wall must be re-probed on the
// schedule instead of idling out the capped twelve-hour window: the run keeps
// probing until upstream recovers, then clears through the generation-bound CAS.
func TestOpenCodeFreeQuotaVerifyReprobesWhileParkHolds(t *testing.T) {
	stubOpenCodeFreeVerifyRetryDelays(t, 20*time.Millisecond, 20*time.Millisecond)

	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			w.Header().Set("X-Sub2api-Transport-Result", "upstream")
			w.Header().Set("X-Sub2api-Dispatched", "true")
			w.Header().Set("Retry-After", "50000")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"FreeUsageLimitError","message":"Free usage limit reached"}}`))
			return
		}
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(quotaVerifyValidAnswerSSE))
	}))
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}

	svc.runOpenCodeFreeQuotaVerify(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, int32(3), hits.Load(), "the run must re-probe a park that still holds")
	require.Equal(t, int32(1), repo.clearCalls.Load(), "recovery must clear the park through the CAS")
}

// A re-probe that would only run once the observed reset has passed is skipped:
// the scheduler already owns that transition, so the run must stop now instead
// of holding the in-flight guard until then.
func TestOpenCodeFreeQuotaVerifyScheduleStopsBeforeReset(t *testing.T) {
	stubOpenCodeFreeVerifyRetryDelays(t, time.Hour)

	var hits atomic.Int32
	worker := quotaVerifyConfirmedWorker(&hits)
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}

	svc.runOpenCodeFreeQuotaVerify(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, int32(1), hits.Load(), "a delay crossing the reset must not dispatch another probe")
	require.Equal(t, int32(0), repo.clearCalls.Load(), "a confirmed wall must keep the park")
}

// Once the schedule is exhausted the run gives up until a later park starts a
// fresh one, and the in-flight guard is released so that later park can schedule.
func TestScheduleOpenCodeFreeQuotaVerifyStopsWhenScheduleExhausted(t *testing.T) {
	stubOpenCodeFreeVerifyRetryDelays(t, 10*time.Millisecond, 10*time.Millisecond)

	var hits atomic.Int32
	worker := quotaVerifyConfirmedWorker(&hits)
	defer worker.Close()

	repo := &quotaVerifyRepoStub{
		row:     quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(30*time.Minute)),
		cleared: true,
	}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 91, Platform: PlatformOpenCodeGo}

	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Eventually(t, func() bool { return hits.Load() == 3 }, 3*time.Second, 5*time.Millisecond,
		"the schedule must probe once immediately and once per delay entry")
	require.Eventually(t, func() bool {
		_, loaded := openCodeFreeQuotaVerifyInFlight.Load(int64(91))
		return !loaded
	}, 3*time.Second, 5*time.Millisecond, "the in-flight guard must release when the schedule ends")
	require.Equal(t, int32(0), repo.clearCalls.Load(), "an exhausted schedule must keep the confirmed park")
}

// A valid-looking prefix cannot clear a cooldown while the upstream stream is
// still open. The original dispatch deadline must also bound the EOF check.
func TestOpenCodeFreeQuotaVerifyKeepsParkedWhenStreamNeverCompletes(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(quotaVerifyValidAnswerSSE + strings.Repeat(": keepalive padding\n\n", 420)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer worker.Close()
	repo := &quotaVerifyRepoStub{row: quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(time.Hour)), cleared: true}
	svc := &RateLimitService{accountRepo: repo}
	cfg := quotaVerifyTestConfig(worker.URL)
	cfg.RequestTimeoutSeconds = 1

	outcome, _ := svc.verifyOpenCodeFreeQuota(1, "big-pickle", cfg)

	require.Equal(t, int32(0), repo.clearCalls.Load(), "a stream without EOF must retain the cooldown")
	require.Equal(t, openCodeFreeVerifyParked, outcome)
}

// A rejected CAS means the generation changed after the probe observed it.
// The run must preserve that newer park and reload its reset on the next probe.
func TestOpenCodeFreeQuotaVerifyKeepsParkedWhenGenerationChanges(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(quotaVerifyValidAnswerSSE))
	}))
	defer worker.Close()
	limited := time.Now().Add(-time.Second)
	reset := time.Now().Add(time.Hour)
	repo := &quotaVerifyRepoStub{row: quotaVerifyParkedRow(limited, reset), cleared: false}
	svc := &RateLimitService{accountRepo: repo}

	outcome, nextReset := svc.verifyOpenCodeFreeQuota(1, "big-pickle", quotaVerifyTestConfig(worker.URL))

	require.Equal(t, int32(1), repo.clearCalls.Load())
	require.True(t, repo.observedLimited.Equal(limited))
	require.True(t, repo.observedReset.Equal(reset))
	require.Equal(t, openCodeFreeVerifyParked, outcome)
	require.Nil(t, nextReset, "a stale generation's reset must not limit the next probe")
}

// A second quota park is coalesced while either the immediate probe or its
// delayed re-probe is running. The guard is released after complete validation.
func TestScheduleOpenCodeFreeQuotaVerifyCoalescesWholeSchedule(t *testing.T) {
	stubOpenCodeFreeVerifyRetryDelays(t, 30*time.Millisecond)
	var hits atomic.Int32
	first := make(chan struct{})
	firstRelease := make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(firstRelease) })
	second := make(chan struct{})
	release := make(chan struct{})
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := hits.Add(1)
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		if count == 1 {
			close(first)
			<-firstRelease
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"FreeUsageLimitError"}}`))
			return
		}
		if count == 2 {
			close(second)
			<-release
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(quotaVerifyValidAnswerSSE))
	}))
	defer worker.Close()
	repo := &quotaVerifyRepoStub{row: quotaVerifyParkedRow(time.Now().Add(-time.Second), time.Now().Add(time.Hour)), cleared: true}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 192, Platform: PlatformOpenCodeGo}
	cfg := quotaVerifyTestConfig(worker.URL)
	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", cfg)
	defer func() {
		releaseFirst()
		close(release)
		require.Eventually(t, func() bool {
			_, loaded := openCodeFreeQuotaVerifyInFlight.Load(account.ID)
			return !loaded
		}, 3*time.Second, time.Millisecond, "wait for schedule exit before restoring global test delays")
	}()
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("immediate probe did not run")
	}
	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", cfg)
	require.Never(t, func() bool { return hits.Load() > 1 }, 10*time.Millisecond, time.Millisecond,
		"a quota park during the immediate probe must reuse the in-flight schedule")
	releaseFirst()
	select {
	case <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("delayed probe did not run")
	}
	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", cfg)
	require.Never(t, func() bool { return hits.Load() > 2 }, 10*time.Millisecond, time.Millisecond,
		"a quota park during the delayed probe must reuse the in-flight schedule")
	require.Equal(t, int32(2), hits.Load(), "overlapping quota parks must not spawn another probe run")
	require.Equal(t, int32(0), repo.clearCalls.Load(), "the delayed probe has not completed validation")
}

// quotaVerifyGenerationRepo models the repository CAS and gates its first
// successful return after the write has applied, so a new park can be persisted
// while the old verification run still owns the in-flight guard.
type quotaVerifyGenerationRepo struct {
	AccountRepository
	mu           sync.Mutex
	row          *Account
	clearedFirst chan struct{}
	releaseFirst chan struct{}
	clearCalls   atomic.Int32
}

func (s *quotaVerifyGenerationRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := *s.row
	return &row, nil
}

func (s *quotaVerifyGenerationRepo) ClearRateLimitIfObservedOpenCodeFree(ctx context.Context, id int64, limitedAt, resetAt time.Time) (bool, error) {
	s.mu.Lock()
	if s.row.RateLimitedAt == nil || s.row.RateLimitResetAt == nil || !s.row.RateLimitedAt.Equal(limitedAt) || !s.row.RateLimitResetAt.Equal(resetAt) {
		s.mu.Unlock()
		return false, nil
	}
	row := *s.row
	row.RateLimitedAt, row.RateLimitResetAt = nil, nil
	s.row = &row
	first := s.clearCalls.Add(1) == 1
	s.mu.Unlock()
	if first {
		close(s.clearedFirst)
		<-s.releaseFirst
	}
	return true, nil
}

func (s *quotaVerifyGenerationRepo) park(row *Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.row = row
}

// A new quota generation can arrive after the old CAS clears the row but before
// that old run releases its guard. Coalescing must remember that wakeup and
// dispatch another probe, rather than silently leave the new park unverified.
func TestScheduleOpenCodeFreeQuotaVerifyRechecksParkAfterTerminalCAS(t *testing.T) {
	stubOpenCodeFreeVerifyRetryDelays(t)
	var hits atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Sub2api-Transport-Result", "upstream")
		w.Header().Set("X-Sub2api-Dispatched", "true")
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(quotaVerifyValidAnswerSSE))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"FreeUsageLimitError"}}`))
	}))
	defer worker.Close()
	limited := time.Now().Add(-time.Second)
	reset := time.Now().Add(time.Hour)
	repo := &quotaVerifyGenerationRepo{
		row:          quotaVerifyParkedRow(limited, reset),
		clearedFirst: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	releaseCAS := sync.OnceFunc(func() { close(repo.releaseFirst) })
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 293, Platform: PlatformOpenCodeGo}
	cfg := quotaVerifyTestConfig(worker.URL)
	defer func() {
		releaseCAS()
		require.Eventually(t, func() bool {
			_, loaded := openCodeFreeQuotaVerifyInFlight.Load(account.ID)
			return !loaded
		}, 3*time.Second, time.Millisecond, "wait for all runs before restoring test delays")
	}()
	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", cfg)
	select {
	case <-repo.clearedFirst:
	case <-time.After(3 * time.Second):
		t.Fatal("first generation was not cleared")
	}
	newLimited, newReset := limited.Add(time.Second), reset.Add(time.Hour)
	repo.park(quotaVerifyParkedRow(newLimited, newReset))
	svc.ScheduleOpenCodeFreeQuotaVerify(account, "big-pickle", cfg)
	releaseCAS()

	require.Eventually(t, func() bool { return hits.Load() == 2 }, 3*time.Second, time.Millisecond,
		"a park arriving during the old terminal CAS must receive a new probe")
	require.Eventually(t, func() bool {
		_, loaded := openCodeFreeQuotaVerifyInFlight.Load(account.ID)
		return !loaded
	}, 3*time.Second, time.Millisecond)
	row, err := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, row.RateLimitedAt)
	require.NotNil(t, row.RateLimitResetAt)
	require.True(t, row.RateLimitedAt.Equal(newLimited), "the old CAS must not erase the new quota generation")
	require.True(t, row.RateLimitResetAt.Equal(newReset))
	require.Equal(t, int32(1), repo.clearCalls.Load(), "the second generation's confirmed quota cannot clear")
}
