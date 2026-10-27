//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func freeAccountCredentials() map[string]any {
	return map[string]any{"account_mode": "zen", "api_protocol": "adaptive", "base_url": "https://opencode.ai/inference/openai/v1", "api_key": "public", "opencode_free_transport": "bun", "opencode_free_profile": "opencode-big-pickle-text-balanced-20261027-v2"}
}

func TestOpenCodeFreeCreateRejectsInvalidAccountBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"private credentials", func(c map[string]any) { c["api_key"] = "private-test-key" }},
		{"go plan", func(c map[string]any) { c["account_mode"] = "go" }},
		{"unknown profile", func(c map[string]any) { c["opencode_free_profile"] = "not-the-validated-profile" }},
		{"Anthropic protocol", func(c map[string]any) { c["api_protocol"] = "anthropic" }},
		{"Responses protocol", func(c map[string]any) { c["api_protocol"] = "responses" }},
		{"custom adaptive chat URL", func(c map[string]any) {
			c["api_base_urls"] = map[string]any{"chat_completions": "https://relay.example/v1"}
		}},
		{"custom pinned chat URL", func(c map[string]any) {
			c["api_protocol"] = "chat_completions"
			c["base_url"] = "https://relay.example/v1"
		}},
		{"legacy Zen URL", func(c map[string]any) { c["base_url"] = "https://opencode.ai/zen/v1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds := freeAccountCredentials()
			tc.change(creds)
			_, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: creds}, nil)
			require.Error(t, err)
		})
	}
}

func TestOpenCodeFreeCreateKeepsUnrelatedProtocolConfiguration(t *testing.T) {
	creds := freeAccountCredentials()
	creds["api_base_urls"] = map[string]any{"chat_completions": "https://opencode.ai/inference/openai/v1/", "anthropic": "https://relay.example/anthropic"}
	a, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: creds}, nil)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example/anthropic", a.Credentials["api_base_urls"].(map[string]any)["anthropic"])
}

func TestOpenCodeFreeUpdateValidatesMergedHiddenKey(t *testing.T) {
	creds := freeAccountCredentials()
	creds["api_key"] = "private-test-key"
	repo := &accountBillingSettingsAdminRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{501: {ID: 501, Name: "before", Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: creds, Status: StatusActive}}}}
	svc := &adminServiceImpl{accountRepo: repo}
	incoming := freeAccountCredentials()
	delete(incoming, "api_key")
	_, err := svc.UpdateAccount(context.Background(), 501, &UpdateAccountInput{Name: "after", Credentials: incoming})
	require.Error(t, err)
	require.Zero(t, repo.updateCalls)
}

func TestOpenCodeFreeUpdateChecksConfigurationOnMetadataOnlyEdit(t *testing.T) {
	creds := freeAccountCredentials()
	creds["api_key"] = "private-test-key"
	repo := &accountBillingSettingsAdminRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{502: {ID: 502, Name: "before", Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: creds, Status: StatusActive}}}}
	_, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 502, &UpdateAccountInput{Name: "after"})
	require.Error(t, err)
	require.Zero(t, repo.updateCalls)
}

func TestOpenCodeFreeDoesNotValidateOtherPlatformsCustomFields(t *testing.T) {
	creds := freeAccountCredentials()
	creds["api_key"] = "other-provider-key"
	a, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: creds}, nil)
	require.NoError(t, err)
	require.Equal(t, "other-provider-key", a.Credentials["api_key"])
}
