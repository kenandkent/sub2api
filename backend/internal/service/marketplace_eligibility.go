package service

// MarketplaceEligibility contains only verified, current dispatch conditions.
// The caller must re-read these immediately before dispatch, including on sticky paths.
type MarketplaceEligibility struct {
	DispatchEnabled, SupplierActive, OfferApproved, OfferEnabled bool
	AccountHealthy, ModelAllowed, GroupAllowed, Shadow           bool
	OwnerUserID, BuyerUserID                                     int64
}

func (e MarketplaceEligibility) Eligible() bool {
	return e.DispatchEnabled && e.SupplierActive && e.OfferApproved && e.OfferEnabled &&
		e.AccountHealthy && e.ModelAllowed && e.GroupAllowed && !e.Shadow &&
		e.OwnerUserID > 0 && e.BuyerUserID > 0 && e.OwnerUserID != e.BuyerUserID
}
