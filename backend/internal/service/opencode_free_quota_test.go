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
	if r.err != nil {
		return r.err
	}
	r.id, r.until = id, resetAt
	return nil
}

func TestApplyOpenCodeFreeQuotaCooldownWindows(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenCodeGo}
	t.Run("uses retry-after when present", func(t *testing.T) {
		repo := &quotaCooldownRepoStub{}
		headers := http.Header{"Retry-After": []string{"74005"}}
		var notifiedUntil time.Time
		until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, headers, func(_ *Account, u time.Time, reason string) {
			require.Equal(t, "429", reason)
			notifiedUntil = u
		})
		require.Equal(t, int64(7), repo.id)
		require.WithinDuration(t, time.Now().Add(74005*time.Second), until, 30*time.Second)
		require.Equal(t, until, repo.until)
		require.Equal(t, until, notifiedUntil)
	})
	t.Run("falls back without retry-after", func(t *testing.T) {
		repo := &quotaCooldownRepoStub{}
		until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, nil, nil)
		require.Equal(t, int64(7), repo.id)
		require.WithinDuration(t, time.Now().Add(time.Hour), until, 30*time.Second)
	})
	t.Run("clamps tiny retry-after to the floor", func(t *testing.T) {
		repo := &quotaCooldownRepoStub{}
		headers := http.Header{"Retry-After": []string{"10"}}
		until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, headers, nil)
		require.WithinDuration(t, time.Now().Add(openCodeFreeQuotaCooldownMin), until, 30*time.Second)
		require.False(t, until.Before(time.Now().Add(openCodeFreeQuotaCooldownMin-time.Minute)))
	})
	t.Run("clamps absurd retry-after to the ceiling", func(t *testing.T) {
		repo := &quotaCooldownRepoStub{}
		headers := http.Header{"Retry-After": []string{"999999999"}}
		until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, headers, nil)
		require.WithinDuration(t, time.Now().Add(openCodeFreeQuotaCooldownMax), until, 30*time.Second)
	})
	t.Run("repo failure returns zero", func(t *testing.T) {
		repo := &quotaCooldownRepoStub{err: context.DeadlineExceeded}
		until := applyOpenCodeFreeQuotaCooldown(context.Background(), repo, account, nil, nil)
		require.True(t, until.IsZero())
	})
}

func TestHandleOpenCodeFreeQuota429Guards(t *testing.T) {
	var nilService *RateLimitService
	require.True(t, nilService.HandleOpenCodeFreeQuota429(context.Background(), &Account{ID: 1}, nil).IsZero())
	require.True(t, (&RateLimitService{}).HandleOpenCodeFreeQuota429(context.Background(), &Account{ID: 1}, nil).IsZero())
	require.True(t, (&RateLimitService{}).HandleOpenCodeFreeQuota429(context.Background(), nil, nil).IsZero())

	repo := &quotaCooldownRepoStub{}
	service := &RateLimitService{accountRepo: repo}
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
