package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"testing"
)

func TestSupplierAccountProviderValidation(t *testing.T) {
	p := config.MarketplaceProviderConfig{Platform: "openai", Label: "OpenAI", BaseURL: "https://api.openai.com/v1", AccountTypes: []string{"apikey"}, Models: []string{"gpt-4o"}}
	if err := validateSupplierProvider(p); err != nil {
		t.Fatal(err)
	}
	p.BaseURL = "http://127.0.0.1/"
	if err := validateSupplierProvider(p); err == nil {
		t.Fatal("unsafe provider URL accepted")
	}
}

func TestSupplierAccountModelsWhitelist(t *testing.T) {
	p := config.MarketplaceProviderConfig{Models: []string{"a", "b"}}
	if !validSupplierModels(p, []string{"a"}) {
		t.Fatal("allowed model rejected")
	}
	for _, models := range [][]string{nil, {"c"}, {"a", "a"}} {
		if validSupplierModels(p, models) {
			t.Fatalf("invalid models accepted: %v", models)
		}
	}
}

func TestSupplierAccountProbeRejectsUnsafeDestinations(t *testing.T) {
	for _, raw := range []string{"http://example.com/v1", "https://user:password@example.com/v1", "https://127.0.0.1/v1"} {
		if err := probeOpenAIModels(context.Background(), raw, "sk-probe"); err == nil {
			t.Fatalf("unsafe destination accepted: %s", raw)
		}
	}
}

func TestSupplierAccountKeyRejectsControlCharacters(t *testing.T) {
	if !validSupplierAPIKey("sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatal("valid key rejected")
	}
	for _, key := range []string{"short", "sk-abcdefghijklmnopqrstuvwxyz\n0123456789", "sk-abcdefghijklmnopqrstuvwxyz 0123456789"} {
		if validSupplierAPIKey(key) {
			t.Fatal("invalid key accepted")
		}
	}
}
