package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsOpenCodeFreeQuotaExhausted(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want bool
	}{
		{"code and message markers", http.StatusTooManyRequests, `{"error":{"code":"FreeUsageLimitError","message":"Free usage limit reached"}}`, true},
		{"message marker only", http.StatusTooManyRequests, `{"error":{"message":"Free usage limit reached"}}`, true},
		{"code marker only", http.StatusTooManyRequests, `{"error":{"code":"FreeUsageLimitError"}}`, true},
		{"generic rate limit", http.StatusTooManyRequests, `{"error":{"code":"rate_limit","message":"try later"}}`, false},
		{"quota marker on non-429", http.StatusBadRequest, `{"error":{"code":"FreeUsageLimitError"}}`, false},
		{"empty body", http.StatusTooManyRequests, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isOpenCodeFreeQuotaExhausted(tc.code, []byte(tc.body)))
		})
	}
}

type quotaCooldownRepoStub struct {
	AccountRepository
	id    int64
	until time.Time
	err   error
}

func (r *quotaCooldownRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.id, r.until = id, resetAt
	return r.err
}

func TestApplyOpenCodeFreeQuotaCooldownWindows(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenCodeGo}
	dateBase := time.Now().UTC().Truncate(time.Second)
	shortDate := dateBase.Add(6 * time.Hour)
	boundaryDate := dateBase.Add(12 * time.Hour)
	for _, tc := range []struct {
		name         string
		headers      http.Header
		wantDuration time.Duration
		wantDate     time.Time
	}{
		{name: "falls back twelve hours without headers", wantDuration: 12 * time.Hour},
		{name: "falls back twelve hours without retry-after", headers: http.Header{}, wantDuration: 12 * time.Hour},
		{name: "falls back twelve hours for empty retry-after", headers: http.Header{"Retry-After": {""}}, wantDuration: 12 * time.Hour},
		{name: "falls back twelve hours for invalid retry-after", headers: http.Header{"Retry-After": {"not-a-date"}}, wantDuration: 12 * time.Hour},
		{name: "honors thirty-minute retry-after", headers: http.Header{"Retry-After": {"1800"}}, wantDuration: 30 * time.Minute},
		{name: "honors three-hour retry-after", headers: http.Header{"Retry-After": {"10800"}}, wantDuration: 3 * time.Hour},
		{name: "honors retry-after at twelve-hour boundary", headers: http.Header{"Retry-After": {"43200"}}, wantDuration: 12 * time.Hour},
		{name: "caps retry-after above twelve-hour boundary", headers: http.Header{"Retry-After": {"43201"}}, wantDuration: 12 * time.Hour},
		{name: "caps next-day retry-after at twelve hours", headers: http.Header{"Retry-After": {"74005"}}, wantDuration: 12 * time.Hour},
		{name: "caps absurd retry-after at twelve hours", headers: http.Header{"Retry-After": {"999999999"}}, wantDuration: 12 * time.Hour},
		{name: "clamps tiny retry-after to one minute", headers: http.Header{"Retry-After": {"10"}}, wantDuration: time.Minute},
		{name: "clamps zero retry-after to one minute", headers: http.Header{"Retry-After": {"0"}}, wantDuration: time.Minute},
		{name: "clamps negative retry-after to one minute", headers: http.Header{"Retry-After": {"-30"}}, wantDuration: time.Minute},
		{name: "honors shorter HTTP-date", headers: http.Header{"Retry-After": {shortDate.Format(http.TimeFormat)}}, wantDate: shortDate},
		{name: "honors HTTP-date at twelve-hour boundary", headers: http.Header{"Retry-After": {boundaryDate.Format(http.TimeFormat)}}, wantDate: boundaryDate},
		{name: "caps HTTP-date above twelve-hour boundary", headers: http.Header{"Retry-After": {dateBase.Add(12*time.Hour + time.Minute).Format(http.TimeFormat)}}, wantDuration: 12 * time.Hour},
		{name: "clamps near HTTP-date to one minute", headers: http.Header{"Retry-After": {dateBase.Add(30 * time.Second).Format(http.TimeFormat)}}, wantDuration: time.Minute},
		{name: "clamps past HTTP-date to one minute", headers: http.Header{"Retry-After": {dateBase.Add(-time.Hour).Format(http.TimeFormat)}}, wantDuration: time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &quotaCooldownRepoStub{}
			var notifiedAccount *Account
			var notifiedUntil time.Time
			var notifiedReason string
			notifyCalls := 0
			before := time.Now()
			until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, tc.headers, func(a *Account, u time.Time, reason string) {
				notifiedAccount, notifiedUntil, notifiedReason = a, u, reason
				notifyCalls++
			})
			after := time.Now()
			require.Equal(t, int64(7), repo.id)
			if !tc.wantDate.IsZero() {
				require.True(t, until.Equal(tc.wantDate), "reset %s should honor HTTP-date %s", until, tc.wantDate)
			} else {
				require.False(t, until.Before(before.Add(tc.wantDuration)), "reset %s is earlier than the required %s window", until, tc.wantDuration)
				require.False(t, until.After(after.Add(tc.wantDuration)), "reset %s exceeds the required %s window", until, tc.wantDuration)
			}
			require.Equal(t, until, repo.until)
			require.Equal(t, 1, notifyCalls)
			require.Same(t, account, notifiedAccount)
			require.Equal(t, "429", notifiedReason)
			require.Equal(t, until, notifiedUntil)
		})
	}
	t.Run("persists twelve-hour fallback without a notifier", func(t *testing.T) {
		repo := &quotaCooldownRepoStub{}
		before := time.Now()
		until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, nil, nil)
		after := time.Now()
		require.Equal(t, int64(7), repo.id)
		require.Equal(t, until, repo.until)
		require.False(t, until.Before(before.Add(12*time.Hour)))
		require.False(t, until.After(after.Add(12*time.Hour)))
	})
	t.Run("repo failure returns zero after notifying the same deadline", func(t *testing.T) {
		repo := &quotaCooldownRepoStub{err: context.DeadlineExceeded}
		var notifiedUntil time.Time
		before := time.Now()
		until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, nil, func(_ *Account, u time.Time, _ string) {
			notifiedUntil = u
		})
		after := time.Now()
		require.True(t, until.IsZero())
		require.Equal(t, int64(7), repo.id)
		require.Equal(t, repo.until, notifiedUntil)
		require.False(t, notifiedUntil.Before(before.Add(12*time.Hour)))
		require.False(t, notifiedUntil.After(after.Add(12*time.Hour)))
	})
}

func TestHandleOpenCodeFreeQuota429Guards(t *testing.T) {
	var nilService *RateLimitService
	require.True(t, nilService.HandleOpenCodeFreeQuota429(context.Background(), &Account{ID: 1}, nil).IsZero())
	require.True(t, (&RateLimitService{}).HandleOpenCodeFreeQuota429(context.Background(), &Account{ID: 1}, nil).IsZero())
	require.True(t, (&RateLimitService{}).HandleOpenCodeFreeQuota429(context.Background(), nil, nil).IsZero())

	repo := &quotaCooldownRepoStub{}
	service := &RateLimitService{accountRepo: repo}
	for _, account := range []*Account{nil, {ID: 0}, {ID: -1}} {
		notified := false
		require.True(t, service.HandleOpenCodeFreeQuota429(context.Background(), account, nil).IsZero())
		require.True(t, applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, nil, func(*Account, time.Time, string) { notified = true }).IsZero())
		require.False(t, notified)
		require.Zero(t, repo.id)
		require.True(t, repo.until.IsZero())
	}
	require.True(t, applyOpenCodeFreeQuotaCooldown(context.Background(), nil, &Account{ID: 9}, nil, nil).IsZero())
	headers := http.Header{"Retry-After": []string{"3600"}}
	until := service.HandleOpenCodeFreeQuota429(context.Background(), &Account{ID: 9, Platform: PlatformOpenCodeGo}, headers)
	require.Equal(t, int64(9), repo.id)
	require.WithinDuration(t, time.Now().Add(time.Hour), until, 30*time.Second)
}

func TestRedactOpenCodeFreeLogSnippet(t *testing.T) {
	redacted := redactOpenCodeFreeLogSnippet("see http://user:pass@proxy.example:8080 with Bearer abc.def-123 ok")
	require.Contains(t, redacted, "http://***:***@proxy.example:8080")
	require.Contains(t, redacted, "Bearer ***")
	require.NotContains(t, redacted, "user:pass")
	require.NotContains(t, redacted, "abc.def-123")

	long := redactOpenCodeFreeLogSnippet(string(make([]byte, openCodeFreeLogSnippetMax+64)))
	require.Len(t, long, openCodeFreeLogSnippetMax)
}
