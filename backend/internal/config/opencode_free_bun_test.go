package config

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestOpenCodeFreeBunConfigDefaultsAndEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.OpenCodeFreeBun.Enabled)
	require.Equal(t, "http://127.0.0.1:19090", cfg.OpenCodeFreeBun.WorkerEndpoint)
	require.EqualValues(t, 8<<20, cfg.OpenCodeFreeBun.MaxResponseBytes)
	t.Setenv("OPENCODE_FREE_BUN_ENABLED", "true")
	t.Setenv("OPENCODE_FREE_BUN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("OPENCODE_FREE_BUN_REQUEST_TIMEOUT_SECONDS", "42")
	cfg, err = Load()
	require.NoError(t, err)
	require.True(t, cfg.OpenCodeFreeBun.Enabled)
	require.Equal(t, 42, cfg.OpenCodeFreeBun.RequestTimeoutSeconds)
}
func TestOpenCodeFreeBunRejectsNonLoopbackOrShortToken(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("OPENCODE_FREE_BUN_ENABLED", "true")
	t.Setenv("OPENCODE_FREE_BUN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("OPENCODE_FREE_BUN_WORKER_ENDPOINT", "http://example.com:19090")
	_, err := Load()
	require.Error(t, err)
	t.Setenv("OPENCODE_FREE_BUN_WORKER_ENDPOINT", "http://127.0.0.1:19090")
	t.Setenv("OPENCODE_FREE_BUN_TOKEN", "short")
	_, err = Load()
	require.Error(t, err)
}
