package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func deployOpencodeFile(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "opencode", rel))
	require.NoError(t, err)
	return raw
}

// The embedded template copies must never drift from the audited deploy files
// that gen_profiles.py derives every profile from.
func TestOpenCodeFreeTemplatesMatchDeploySources(t *testing.T) {
	require.Equal(t, deployOpencodeFile(t, "balanced-text-profile.v2.json"), []byte(openCodeFreeTextTemplate))
	require.Equal(t, deployOpencodeFile(t, "profiles/big-pickle-agent-balanced.v1.json"), []byte(openCodeFreeAgentTemplate))
}

func openCodeFreePinnedDeployFile(model, mode string) string {
	if mode == openCodeFreeModeText {
		if model == "big-pickle" {
			return "profiles/big-pickle-text-balanced.v2.json"
		}
		return "profiles/" + model + "-text-balanced.v1.json"
	}
	return "profiles/" + model + "-agent-balanced.v1.json"
}

// Every pinned profile ref must hash exactly the bytes deploy/opencode/profiles/
// ships, and the pinned big-pickle text hash must keep the recorded constant.
func TestOpenCodeFreePinnedHashesMatchDeployProfiles(t *testing.T) {
	require.Equal(t, openCodeFreeProfileHash, sha256Hex(deployOpencodeFile(t, "profiles/big-pickle-text-balanced.v2.json")))
	require.Equal(t, "opencode-big-pickle-text-balanced-20261027-v2", openCodeFreeProfile)
	for model, profiles := range openCodeFreeModels {
		for _, pair := range []struct {
			mode string
			ref  openCodeFreeProfileRef
		}{{openCodeFreeModeText, profiles.Text}, {openCodeFreeModeAgent, profiles.Agent}} {
			raw := deployOpencodeFile(t, openCodeFreePinnedDeployFile(model, pair.mode))
			require.Equal(t, pair.ref.SHA256, sha256Hex(raw), model+"/"+pair.mode)
			var data map[string]any
			require.NoError(t, json.Unmarshal(raw, &data), model+"/"+pair.mode)
			require.Equal(t, pair.ref.ID, data["id"], model+"/"+pair.mode)
			require.Equal(t, model, data["model"], model+"/"+pair.mode)
		}
	}
}

// The template substitution must reproduce every derivable pinned profile, so
// template drift or an algorithm change is caught on both sides of the pin.
func TestOpenCodeFreeDerivationMatchesPinnedProfiles(t *testing.T) {
	for model, profiles := range openCodeFreeModels {
		if model == "big-pickle" || model == "mimo-v2.6-flash-free" {
			// big-pickle is the templates themselves; mimo's captured profile
			// intentionally differs (validated_temperature_values).
			continue
		}
		for _, pair := range []struct {
			mode string
			ref  openCodeFreeProfileRef
		}{{openCodeFreeModeText, profiles.Text}, {openCodeFreeModeAgent, profiles.Agent}} {
			derived, _, ok := openCodeFreeDeriveProfile(model, pair.mode)
			require.True(t, ok, model+"/"+pair.mode)
			require.Equal(t, pair.ref, derived, model+"/"+pair.mode)
		}
	}
}

func TestOpenCodeFreeDerivationGoldenValues(t *testing.T) {
	for _, tc := range []struct {
		model string
		mode  string
		id    string
		sha   string
	}{
		{"foo-free", openCodeFreeModeText, "opencode-foo-free-text-balanced-20261027-v1", "dbf2692e129e259fdc39b912a0df94bd65b3c485492ec4d4edd8fff734126886"},
		{"foo-free", openCodeFreeModeAgent, "opencode-foo-free-agent-balanced-20261027-v1", "3f364c46161820efa4a0bbec645d1f9d83a4b0a04e023f29c10645d1c6b77ed3"},
		{"bar-baz-free", openCodeFreeModeText, "opencode-bar-baz-free-text-balanced-20261027-v1", "275620117b22762e0c8fb5d196a904ab38b6f96328e3caa160287c935d8ea882"},
		{"bar-baz-free", openCodeFreeModeAgent, "opencode-bar-baz-free-agent-balanced-20261027-v1", "88238a75ff9f5d5beb68a55aeee17b00510182c28e6f9eaf1543221da4d0cc7b"},
		{"nemotron-9-ultra-free", openCodeFreeModeText, "opencode-nemotron-9-ultra-free-text-balanced-20261027-v1", "f08c5ed33b2094e1f2b04de6d30d995e1835c02ee92e1cea9e6343da31fbb36f"},
		{"nemotron-9-ultra-free", openCodeFreeModeAgent, "opencode-nemotron-9-ultra-free-agent-balanced-20261027-v1", "b83f663413aa2f1aaf779c16b3c1d7b2fc251e0d10da1f1ce851f4b007f93156"},
	} {
		ref, raw, ok := openCodeFreeDeriveProfile(tc.model, tc.mode)
		require.True(t, ok, tc.model+"/"+tc.mode)
		require.Equal(t, tc.id, ref.ID, tc.model+"/"+tc.mode)
		require.Equal(t, tc.sha, ref.SHA256, tc.model+"/"+tc.mode)
		var data map[string]any
		require.NoError(t, json.Unmarshal(raw, &data), tc.model+"/"+tc.mode)
		require.Equal(t, tc.id, data["id"])
		require.Equal(t, tc.model, data["model"])
		require.Equal(t, openCodeFreeDynamicStatus, data["status"])
		body, ok := data["body"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, tc.model, body["model"])
	}
}

func TestOpenCodeFreeAdmission(t *testing.T) {
	for model := range openCodeFreeModels {
		require.True(t, openCodeFreeAdmissible(model), model)
	}
	for _, model := range []string{"foo-free", "x-free", "nemotron-9-ultra-free", "deepseek-v4.2-free", "a.b-c-free"} {
		require.True(t, openCodeFreeAdmissible(model), model)
	}
	for _, model := range []string{
		"",
		"gpt-4o",
		"big-pickle-plus", // no -free suffix and not pinned
		"FOO-free",        // uppercase
		"foo_free",        // invalid separator
		"-free",           // must start alphanumeric
		"foo-free ",       // trailing space
		"jev-1.13-free",   // SystemOne endpoint model
		"gemini-2.5-pro-free",
		"opencode-go/foo-free",
	} {
		require.False(t, openCodeFreeAdmissible(model), model)
	}
	require.False(t, openCodeFreeAdmissible(strings.Repeat("a", 70)+"-free"), "over 64 chars")
}

func TestOpenCodeFreeProfileForFallsBackToDerivation(t *testing.T) {
	ref, ok := openCodeFreeProfileFor("foo-free", openCodeFreeModeText)
	require.True(t, ok)
	require.Equal(t, "opencode-foo-free-text-balanced-20261027-v1", ref.ID)
	require.Equal(t, "dbf2692e129e259fdc39b912a0df94bd65b3c485492ec4d4edd8fff734126886", ref.SHA256)

	// Pinned models must never fall through to derivation.
	ref, ok = openCodeFreeProfileFor("big-pickle", openCodeFreeModeText)
	require.True(t, ok)
	require.Equal(t, openCodeFreeProfileRef{ID: openCodeFreeProfile, SHA256: openCodeFreeProfileHash}, ref)
	ref, ok = openCodeFreeProfileFor("mimo-v2.6-flash-free", openCodeFreeModeAgent)
	require.True(t, ok)
	require.Equal(t, openCodeFreeModels["mimo-v2.6-flash-free"].Agent, ref)

	// Inadmissible and unknown modes stay rejected.
	_, ok = openCodeFreeProfileFor("gpt-4o", openCodeFreeModeText)
	require.False(t, ok)
	_, ok = openCodeFreeProfileFor("jev-1.13-free", openCodeFreeModeText)
	require.False(t, ok)
	_, ok = openCodeFreeProfileFor("foo-free", "stream")
	require.False(t, ok)
}

func TestNormalizeOpenCodeFreeRequestDynamicModel(t *testing.T) {
	req, err := normalizeOpenCodeFreeRequest([]byte(`{"model":"alias","messages":[{"role":"user","content":"hello"}],"stream":true}`), "foo-free")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeText, req.Mode)
	require.Equal(t, "opencode-foo-free-text-balanced-20261027-v1", req.Profile.ID)
	require.Equal(t, "dbf2692e129e259fdc39b912a0df94bd65b3c485492ec4d4edd8fff734126886", req.Profile.SHA256)
	require.True(t, req.Stream)
	var got map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &got))
	require.Equal(t, "foo-free", got["model"])

	agentBody := `{"model":"alias","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`
	req, err = normalizeOpenCodeFreeRequest([]byte(agentBody), "foo-free")
	require.NoError(t, err)
	require.Equal(t, openCodeFreeModeAgent, req.Mode)
	require.Equal(t, "opencode-foo-free-agent-balanced-20261027-v1", req.Profile.ID)
	require.Equal(t, "3f364c46161820efa4a0bbec645d1f9d83a4b0a04e023f29c10645d1c6b77ed3", req.Profile.SHA256)
	require.Equal(t, []string{"lookup"}, req.ToolNames)
	require.Equal(t, []string{"read"}, req.InjectedTools)

	// The gateway maps the requested model; both the original and mapped name
	// must admit, otherwise the request is rejected before dispatch.
	for _, mapped := range []string{"gpt-4o", "jev-1.13-free", "gemini-2.5-pro-free", "FOO-free"} {
		_, err = normalizeOpenCodeFreeRequest([]byte(`{"model":"alias","messages":[{"role":"user","content":"x"}]}`), mapped)
		require.Error(t, err, mapped)
	}
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
