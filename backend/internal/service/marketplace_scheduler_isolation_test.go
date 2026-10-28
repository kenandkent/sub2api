package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Owned accounts must never enter either ordinary scheduler path before the
// marketplace dispatch and billing flow is available.
func TestMarketplaceSchedulerRejectsOwnedAccountInOrdinaryPaths(t *testing.T) {
	owner := int64(7)
	account := &Account{ID: 1, OwnerUserID: &owner, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	ctx := context.Background()
	reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, PlatformOpenAI, "", false, "")
	require.NotEmpty(t, reason)
	scheduler := &defaultOpenAIAccountScheduler{}
	compatible, _ := scheduler.isAccountRequestCompatibleReason(ctx, account, OpenAIAccountScheduleRequest{})
	require.False(t, compatible)
}

func TestMarketplaceSchedulerRejectsShadowOfOwnedAccount(t *testing.T) {
	owner := int64(7)
	parentID := int64(8)
	parent := &Account{ID: parentID, OwnerUserID: &owner, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	shadow := &Account{ID: 9, ParentAccountID: &parentID}
	require.False(t, parentHealthyForShadow(shadow, func(int64) *Account { return parent }))
}
