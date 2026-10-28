//go:build unit

package service

import (
	"context"
	"testing"
)

func TestMarketplaceOwnerTokenRequiresPreparedCredential(t *testing.T) {
	owner := int64(9)
	cache := newOpenAITokenCacheStub()
	p := &OpenAITokenProvider{tokenCache: cache}
	account := &Account{ID: 1, OwnerUserID: &owner, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"version": 1}}
	if _, err := p.GetAccessToken(context.Background(), account); err == nil {
		t.Fatal("encrypted snapshot must not be accepted as a token")
	}
	account.Credentials = map[string]any{"access_token": "prepared-token"}
	token, err := p.GetAccessToken(context.Background(), account)
	if err != nil || token != "prepared-token" {
		t.Fatalf("prepared token: %q %v", token, err)
	}
	if cache.getCalled != 0 || cache.setCalled != 0 || cache.lockCalled != 0 {
		t.Fatal("supplier token must never reach shared cache")
	}
}
