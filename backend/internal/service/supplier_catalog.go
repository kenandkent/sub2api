package service

import "context"

// SupplierCatalog must resolve only published, currently available marketplace prices.
type SupplierCatalog interface {
	Prices(ctx context.Context, platform string, models []string) ([]SupplierModelPrice, error)
}

type SupplierModelPrice struct {
	GroupID        int64                  `json:"group_id"`
	Model          string                 `json:"model"`
	PublicModel    string                 `json:"public_model"`
	GroupName      string                 `json:"group_name"`
	PriceVersion   int64                  `json:"price_version"`
	RateMultiplier string                 `json:"rate_multiplier"`
	Tiers          []MarketplacePriceTier `json:"tiers"`
}
