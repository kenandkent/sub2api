package service

import (
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// validateOpenCodeFreeAccount is deliberately scoped to the OpenCode credential
// namespace. Unrelated platforms may store identically named custom fields.
func validateOpenCodeFreeAccount(account *Account) error {
	if account == nil || account.Platform != PlatformOpenCodeGo {
		return nil
	}
	raw, exists := account.Credentials["opencode_free_transport"]
	if !exists {
		return nil
	}
	transport, ok := raw.(string)
	if !ok {
		return infraerrors.BadRequest("INVALID_OPENCODE_FREE_ACCOUNT", "OpenCode free transport must be a string")
	}
	if transport == "" {
		return nil
	}
	if transport != "bun" || !account.IsOpenCodeZen() || account.Type != AccountTypeAPIKey || account.GetCredential("api_key") != "public" || account.GetCredential("opencode_free_profile") != openCodeFreeProfile {
		return infraerrors.BadRequest("INVALID_OPENCODE_FREE_ACCOUNT", "OpenCode free Bun requires a Zen API key account using public and the validated v2 profile")
	}
	if protocol := account.GetAPIProtocol(); protocol != APIProtocolAdaptive && protocol != APIProtocolChatCompletions {
		return infraerrors.BadRequest("INVALID_OPENCODE_FREE_ACCOUNT", "OpenCode free Bun requires adaptive or Chat Completions protocol")
	}
	base := strings.TrimRight(strings.TrimSpace(account.GetCNProtocolBaseURL(APIProtocolChatCompletions)), "/")
	if !account.IsAdaptiveAPIProtocol() {
		// Pinned Chat accounts retain base_url in the editor. Do not silently
		// ignore an explicit relay or historical URL when selecting fixed Bun.
		if explicit := strings.TrimSpace(account.GetCredential("base_url")); explicit != "" {
			base = strings.TrimRight(explicit, "/")
		}
	}
	if base != DefaultOpenCodeZenBaseURL {
		return infraerrors.BadRequest("INVALID_OPENCODE_FREE_ACCOUNT", "OpenCode free Bun requires the official Console Chat base URL")
	}
	return nil
}
