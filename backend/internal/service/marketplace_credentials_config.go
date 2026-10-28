package service

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Keys must be stable deployment secrets. Disabled installations may omit the
// vault entirely; retained encrypted data still needs its original key on read.
func NewConfiguredMarketplaceEnvelope(cfg *config.Config) (*CredentialEnvelope, error) {
	if cfg == nil {
		return nil, ErrCredentialEnvelope
	}
	c := cfg.Marketplace
	if c.CredentialKeyID == "" && strings.TrimSpace(c.CredentialKeysJSON) == "" && !c.Enabled {
		return nil, nil
	}
	var encoded map[string]string
	if json.Unmarshal([]byte(c.CredentialKeysJSON), &encoded) != nil || len(encoded) == 0 {
		return nil, ErrCredentialEnvelope
	}
	keys := make(map[string][]byte, len(encoded))
	for id, value := range encoded {
		key, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || len(key) != 32 || id == "" {
			return nil, ErrCredentialEnvelope
		}
		keys[id] = key
	}
	return NewCredentialEnvelope(c.CredentialKeyID, keys)
}
