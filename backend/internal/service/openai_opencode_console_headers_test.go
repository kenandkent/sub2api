package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenCodeConsoleRequestPreservesClientSessionMetadata(t *testing.T) {
	account := &Account{ID: 19, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"account_mode": "zen", "base_url": "https://opencode.ai/inference/openai/v1"}}
	c := newOpenCodeSessionTestContext(t, "ses_affinity")
	c.Request.Header.Set("x-opencode-session-id", "ses_child")
	c.Request.Header.Set("x-opencode-parent-session-id", "ses_parent")
	c.Request.Header.Set("x-opencode-project", "project-from-client")
	req, err := openCodeSessionTestService().buildUpstreamRequest(context.Background(), c, account,
		[]byte(`{"model":"beam-free","prompt_cache_key":"ses_affinity","input":"hello"}`), "public", false, "", false)
	require.NoError(t, err)
	require.Equal(t, "Bearer public", req.Header.Get("Authorization"))
	require.Regexp(t, `^opencode/[^/]+/[^/]+/cli$`, req.Header.Get("User-Agent"))
	for name, want := range map[string]string{
		"x-opencode-session": "ses_affinity", "x-opencode-session-id": "ses_child",
		"x-session-affinity": "ses_affinity", "X-Session-Id": "ses_affinity",
		"x-opencode-parent-session-id": "ses_parent", "x-parent-session-id": "ses_parent",
		"x-opencode-project": "project-from-client", "x-opencode-client": "cli",
	} {
		require.Equal(t, want, req.Header.Get(name), name)
	}
}

func TestOpenCodeConsoleGeneratedSessionSurvivesRequestRebuild(t *testing.T) {
	account := &Account{ID: 19, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"account_mode": "zen"}}
	c := newOpenCodeSessionTestContext(t, "")
	build := func() http.Header {
		headers := make(http.Header)
		applyOpenCodeSessionHeader(c, account, "https://opencode.ai/inference/openai/v1/chat/completions", headers)
		return headers
	}
	first, second := build(), build()
	require.NotEmpty(t, first.Get("x-opencode-session"))
	require.Equal(t, first.Get("x-opencode-session"), second.Get("x-opencode-session"))
	require.Equal(t, first.Get("x-opencode-session"), first.Get("x-session-affinity"))
	require.Equal(t, first.Get("x-opencode-session"), first.Get("x-opencode-session-id"))
	require.Empty(t, first.Get("x-opencode-parent-session-id"))
	other := newOpenCodeSessionTestContext(t, "")
	headers := make(http.Header)
	applyOpenCodeSessionHeader(other, account, "https://opencode.ai/inference/openai/v1/chat/completions", headers)
	require.NotEqual(t, first.Get("x-opencode-session"), headers.Get("x-opencode-session"))
}

func TestOpenCodeConsoleMetadataHonorsExplicitAccountHeaders(t *testing.T) {
	account := &Account{ID: 19, Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"account_mode": "zen", "base_url": "https://opencode.ai/inference/openai/v1",
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"user-agent": "custom-client/2", "x-opencode-client": "desktop", "x-opencode-project": "configured-project"},
		}}
	c := newOpenCodeSessionTestContext(t, "session-1")
	c.Request.Header.Set("x-opencode-project", "caller-project")
	req, err := openCodeSessionTestService().buildUpstreamRequest(context.Background(), c, account,
		[]byte(`{"model":"beam-free","input":"hello"}`), "public", false, "", false)
	require.NoError(t, err)
	require.Equal(t, "custom-client/2", getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, "desktop", getHeaderRaw(req.Header, "x-opencode-client"))
	require.Equal(t, "configured-project", getHeaderRaw(req.Header, "x-opencode-project"))
	require.Equal(t, "session-1", req.Header.Get("x-session-affinity"))
}

func TestOpenCodeConsoleMetadataDoesNotLeakToOtherOrigins(t *testing.T) {
	for _, target := range []string{"https://relay.example.com/v1/chat/completions", "https://opencode.ai.evil.example/inference/openai/v1/chat/completions", "http://opencode.ai/inference/openai/v1/chat/completions"} {
		t.Run(target, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			c := newOpenCodeSessionTestContext(t, "private-session")
			c.Request.Header.Set("x-opencode-project", "private-project")
			headers := make(http.Header)
			applyOpenCodeSessionHeader(c, account, target, headers)
			for _, name := range []string{"x-opencode-client", "x-opencode-project", "x-opencode-session-id", "x-session-affinity", "X-Session-Id"} {
				require.Empty(t, headers.Get(name), name)
			}
		})
	}
}

func TestOpenCodeConsoleChildSessionProvidesStableAffinityAcrossTurns(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"account_mode": "zen"}}
	for _, explicitAffinity := range []string{"", "parent-affinity"} {
		t.Run(explicitAffinity, func(t *testing.T) {
			for turn := 0; turn < 2; turn++ {
				c := newOpenCodeSessionTestContext(t, explicitAffinity)
				c.Request.Header.Set("x-opencode-session-id", "ses_stable_child")
				headers := make(http.Header)
				applyOpenCodeSessionHeader(c, account, "https://opencode.ai/inference/openai/v1/chat/completions", headers)
				want := "ses_stable_child"
				if explicitAffinity != "" {
					want = explicitAffinity
				}
				require.Equal(t, "ses_stable_child", headers.Get("x-opencode-session-id"))
				require.Equal(t, want, headers.Get("x-opencode-session"))
				require.Equal(t, want, headers.Get("x-session-affinity"))
			}
		})
	}
}
