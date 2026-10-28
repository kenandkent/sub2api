//go:build unit

package dto

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"testing"
)

func TestMarketplaceOwnerDTOOmitsEnvelope(t *testing.T) {
	owner := int64(3)
	got := AccountFromServiceShallow(&service.Account{OwnerUserID: &owner, Credentials: map[string]any{"_supplier_envelope": map[string]any{"ciphertext": "secret"}, "base_url": "https://api.openai.com"}})
	if _, ok := got.Credentials["_supplier_envelope"]; ok {
		t.Fatal("credential envelope exposed")
	}
}
