//go:build unit

package repository

import (
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"testing"
)

func TestMarketplaceOwnerAccountProjection(t *testing.T) {
	owner := int64(42)
	got := accountEntityToService(&dbent.Account{ID: 1, OwnerUserID: &owner})
	if got.OwnerUserID == nil || *got.OwnerUserID != owner {
		t.Fatalf("owner not preserved: %#v", got.OwnerUserID)
	}
}
