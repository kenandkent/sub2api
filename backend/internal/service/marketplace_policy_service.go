package service

import (
	"context"
	"errors"
	"reflect"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/shopspring/decimal"
)

// MarketplacePolicyService is a fail-closed view of the configured switches.
type MarketplacePolicyTermsSource interface { CurrentPolicy(context.Context) (MarketplacePolicyVersion,error) }
type MarketplacePolicyService struct{ cfg *config.Config; terms MarketplacePolicyTermsSource }

func (s *MarketplacePolicyService) SetTermsSource(source MarketplacePolicyTermsSource) {s.terms=source}

func (s *MarketplacePolicyService) CurrentTerms(ctx context.Context) (MarketplacePolicyVersion,error) {
	if s.terms!=nil{return s.terms.CurrentPolicy(ctx)}
	if !s.enabled(){return MarketplacePolicyVersion{},errors.New("marketplace policy terms unavailable")}
	c:=s.cfg.Marketplace
	minimum,err:=decimal.NewFromString(*c.MinimumPayoutUSD)
	if err!=nil{return MarketplacePolicyVersion{},err}
	return MarketplacePolicyVersion{FeeBPS:*c.FeeBPS,HoldHours:*c.HoldHours,MaxSupplierConcurrency:*c.MaxSupplierConcurrency,MinimumPayoutUSD:Money{Decimal:minimum}},nil
}

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
