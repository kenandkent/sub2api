package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfferIdentityFieldDoesNotClaimVerification(t *testing.T) {
	raw, err := json.Marshal(AdminSupplierOffer{})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"credential_fingerprint_present"`)
	require.NotContains(t, string(raw), `"identity_verified"`)
}
