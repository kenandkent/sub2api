package service

import (
	"errors"
	"reflect"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// MarketplacePolicyService is a fail-closed view of the configured switches.
type MarketplacePolicyService struct{ cfg *config.Config }

func NewMarketplacePolicyService(cfg *config.Config) *MarketplacePolicyService {
	return &MarketplacePolicyService{cfg: cfg}
}
func (s *MarketplacePolicyService) ValidateEnable() error {
	if s == nil || s.cfg == nil {
		return nil
	}
	return s.cfg.Marketplace.Validate(s.cfg.RunMode)
}
func (s *MarketplacePolicyService) enabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Marketplace.Enabled && s.ValidateEnable() == nil
}
func (s *MarketplacePolicyService) OnboardingEnabled() bool {
	return s.enabled() && s.cfg.Marketplace.OnboardingEnabled
}
func (s *MarketplacePolicyService) DispatchEnabled() bool {
	return s.enabled() && s.cfg.Marketplace.DispatchEnabled
}
func (s *MarketplacePolicyService) PayoutsEnabled() bool {
	return s.enabled() && s.cfg.Marketplace.PayoutsEnabled
}

// ValidateMarketplacePolicyUpdate rejects mutation of a published snapshot.
func ValidateMarketplacePolicyUpdate(previous, next MarketplacePolicyVersion) error {
	if previous.PublishedAt != nil && !reflect.DeepEqual(previous, next) {
		return errors.New("published marketplace policy is immutable; create a new version")
	}
	return nil
}
