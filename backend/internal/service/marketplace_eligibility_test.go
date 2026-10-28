//go:build unit

package service

import "testing"

func TestMarketplaceEligibilityRequiresAllGates(t *testing.T) {
	in := MarketplaceEligibility{DispatchEnabled: true, SupplierActive: true, OfferApproved: true, OfferEnabled: true, AccountHealthy: true, ModelAllowed: true, GroupAllowed: true, OwnerUserID: 1, BuyerUserID: 2}
	if !in.Eligible() {
		t.Fatal("eligible offer rejected")
	}
	for name, mutate := range map[string]func(*MarketplaceEligibility){
		"global":        func(v *MarketplaceEligibility) { v.DispatchEnabled = false },
		"supplier":      func(v *MarketplaceEligibility) { v.SupplierActive = false },
		"review":        func(v *MarketplaceEligibility) { v.OfferApproved = false },
		"paused":        func(v *MarketplaceEligibility) { v.OfferEnabled = false },
		"health":        func(v *MarketplaceEligibility) { v.AccountHealthy = false },
		"model":         func(v *MarketplaceEligibility) { v.ModelAllowed = false },
		"group":         func(v *MarketplaceEligibility) { v.GroupAllowed = false },
		"self":          func(v *MarketplaceEligibility) { v.BuyerUserID = v.OwnerUserID },
		"shadow":        func(v *MarketplaceEligibility) { v.Shadow = true },
		"missing buyer": func(v *MarketplaceEligibility) { v.BuyerUserID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			v := in
			mutate(&v)
			if v.Eligible() {
				t.Fatal("unsafe candidate admitted")
			}
		})
	}
}
