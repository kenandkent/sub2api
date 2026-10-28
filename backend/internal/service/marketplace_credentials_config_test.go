package service

import (
	"encoding/base64"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestConfiguredMarketplaceEnvelope(t *testing.T) {
	_, err := NewConfiguredMarketplaceEnvelope(&config.Config{Marketplace: config.MarketplaceConfig{Enabled: true}})
	require.Error(t, err)
	envelope, err := NewConfiguredMarketplaceEnvelope(&config.Config{})
	require.NoError(t, err)
	require.Nil(t, envelope)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{CredentialKeyID: "v1", CredentialKeysJSON: `{"v1":"` + key + `"}`}}
	envelope, err = NewConfiguredMarketplaceEnvelope(cfg)
	require.NoError(t, err)
	sealed, err := envelope.Seal("openai", 1, 2, []byte("secret"))
	require.NoError(t, err)
	plain, err := envelope.Open("openai", 1, 2, sealed)
	require.NoError(t, err)
	require.Equal(t, "secret", string(plain))
	cfg.Marketplace.CredentialKeysJSON = `{"v1":"not-a-key"}`
	_, err = NewConfiguredMarketplaceEnvelope(cfg)
	require.ErrorIs(t, err, ErrCredentialEnvelope)
}
