package service

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type SupplierProfile struct {
	UserID              int64             `json:"user_id"`
	Status              SupplierStatus    `json:"status"`
	ReviewReason        string            `json:"review_reason"`
	AgreementVersion    int64             `json:"agreement_version"`
	AgreementAcceptedAt *time.Time        `json:"agreement_accepted_at,omitempty"`
	ApplicationFields   map[string]string `json:"application_fields"`
	PayoutFrozen        bool              `json:"payout_frozen"`
	Version             int64             `json:"version"`
}

// SupplierAccount is intentionally a whitelist: credentials and platform controls never leave this API.
type SupplierAccount struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Type     string `json:"type"`
	Status   string `json:"status"`
}
type UserCapabilities struct {
	Consumer           bool     `json:"consumer"`
	SupplierStatus     string   `json:"supplier_status"`
	MarketplaceEnabled bool     `json:"marketplace_enabled"`
	AllowedWorkspaces  []string `json:"allowed_workspaces"`
}
type SupplierRepository interface {
	GetProfile(context.Context, int64) (*SupplierProfile, error)
	Apply(context.Context, int64, int64, map[string]string) (*SupplierProfile, error)
	Review(context.Context, int64, int64, SupplierStatus, string) (*SupplierProfile, error)
	ListProfiles(context.Context, int, int) ([]SupplierProfile, int64, error)
	ListAccounts(context.Context, int64) ([]SupplierAccount, error)
	GetAccount(context.Context, int64, int64) (*SupplierAccount, error)
}
type SupplierService struct {
	repo SupplierRepository
	cfg  *config.Config
}

func NewSupplierService(repo SupplierRepository, cfg *config.Config) *SupplierService {
	return &SupplierService{repo: repo, cfg: cfg}
}
func (s *SupplierService) enabled() bool {
	return s.cfg != nil && s.cfg.Marketplace.Enabled && config.NormalizeRunMode(s.cfg.RunMode) == config.RunModeStandard
}
func (s *SupplierService) onboarding() bool {
	return s.enabled() && s.cfg.Marketplace.OnboardingEnabled
}
func (s *SupplierService) Capabilities(ctx context.Context, user int64) (UserCapabilities, error) {
	c := UserCapabilities{Consumer: true, SupplierStatus: "not_applied", MarketplaceEnabled: s.enabled(), AllowedWorkspaces: []string{"consumer"}}
	p, err := s.repo.GetProfile(ctx, user)
	if err != nil {
		return c, err
	}
	if p != nil {
		c.SupplierStatus = string(p.Status)
	}
	if p != nil || s.onboarding() {
		c.AllowedWorkspaces = append(c.AllowedWorkspaces, "supplier")
	}
	return c, nil
}
func (s *SupplierService) SupplierCapability(ctx context.Context, user int64) (SupplierCapability, error) {
	p, err := s.repo.GetProfile(ctx, user)
	c := SupplierCapability{UserID: user}
	if err != nil {
		return c, err
	}
	if p == nil {
		c.CanApply = s.onboarding()
		return c, nil
	}
	c.Status = p.Status
	c.PayoutFrozen = p.PayoutFrozen
	c.CanReadHistory = true
	c.CanApply = s.onboarding() && (p.Status == SupplierStatusPending || p.Status == SupplierStatusRejected)
	c.CanManageOffers = s.enabled() && p.Status == SupplierStatusActive
	return c, nil
}
func (s *SupplierService) Profile(ctx context.Context, user int64) (*SupplierProfile, error) {
	p, e := s.repo.GetProfile(ctx, user)
	if e != nil {
		return nil, e
	}
	if p == nil {
		return nil, apperrors.NotFound("supplier_not_found", "Supplier profile not found")
	}
	return p, nil
}
func (s *SupplierService) Apply(ctx context.Context, user, agreement int64, fields map[string]string) (*SupplierProfile, error) {
	if !s.onboarding() {
		return nil, apperrors.Forbidden("marketplace_disabled", "Supplier onboarding is disabled")
	}
	if !validSupplierAgreement(s.cfg.Marketplace.AgreementText, s.cfg.Marketplace.AgreementURL) {
		return nil, apperrors.Forbidden("supplier_agreement_unavailable", "Supplier agreement is unavailable")
	}
	if user <= 0 || s.cfg.Marketplace.AgreementVersion <= 0 || agreement != s.cfg.Marketplace.AgreementVersion {
		return nil, apperrors.BadRequest("invalid_agreement", "Current agreement must be accepted")
	}
	if fields == nil {
		fields = map[string]string{}
	}
	allowed := map[string]bool{}
	for _, name := range s.cfg.Marketplace.ApplicationFields {
		allowed[name] = true
	}
	if len(fields) != len(allowed) {
		return nil, apperrors.BadRequest("invalid_application", "Required application fields are missing")
	}
	for k, v := range fields {
		l := strings.ToLower(k)
		if !allowed[k] || strings.Contains(l, "secret") || strings.Contains(l, "token") || strings.Contains(l, "password") || strings.Contains(l, "credential") || strings.Contains(l, "key") || strings.Contains(l, "authorization") || strings.TrimSpace(v) == "" || len(v) > 1000 {
			return nil, apperrors.BadRequest("invalid_application", "Invalid application fields")
		}
	}
	p, err := s.repo.GetProfile(ctx, user)
	if err != nil {
		return nil, err
	}
	if p != nil && p.Status != SupplierStatusPending && p.Status != SupplierStatusRejected {
		return nil, apperrors.Forbidden("supplier_not_eligible", "Supplier cannot apply in current status")
	}
	return s.repo.Apply(ctx, user, agreement, fields)
}
func (s *SupplierService) Review(ctx context.Context, user, admin int64, status SupplierStatus, reason string) (*SupplierProfile, error) {
	reason = strings.TrimSpace(reason)
	if user <= 0 || admin <= 0 || reason == "" || len(reason) > 1000 || (status != SupplierStatusActive && status != SupplierStatusRejected && status != SupplierStatusSuspended) {
		return nil, apperrors.BadRequest("invalid_review", "Valid status and reason are required")
	}
	return s.repo.Review(ctx, user, admin, status, reason)
}
func (s *SupplierService) ListProfiles(ctx context.Context, page, pageSize int) ([]SupplierProfile, int64, error) {
	return s.repo.ListProfiles(ctx, page, pageSize)
}
func (s *SupplierService) ListAccounts(ctx context.Context, user int64) ([]SupplierAccount, error) {
	p, err := s.repo.GetProfile(ctx, user)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperrors.Forbidden("supplier_required", "Supplier profile required")
	}
	return s.repo.ListAccounts(ctx, user)
}
func (s *SupplierService) GetAccount(ctx context.Context, user, id int64) (*SupplierAccount, error) {
	p, err := s.repo.GetProfile(ctx, user)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperrors.NotFound("account_not_found", "Account not found")
	}
	return s.repo.GetAccount(ctx, user, id)
}

func validSupplierAgreement(text, raw string) bool {
	text = strings.TrimSpace(text)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return text != ""
	}
	if strings.ContainsAny(raw, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsAny(u.Path, "\\\r\n\t") || strings.HasPrefix(u.Path, "//") {
		return false
	}
	if u.IsAbs() {
		return u.Scheme == "https" && u.Host != "" && u.User == nil
	}
	return strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && u.Host == ""
}

type SupplierOptions struct {
	Providers              []SupplierProviderOption `json:"providers"`
	AgreementText          string                   `json:"agreement_text"`
	AgreementURL           string                   `json:"agreement_url"`
	AgreementVersion       int64                    `json:"agreement_version"`
	ApplicationFields      []string                 `json:"application_fields"`
	MaxSupplierConcurrency *int                     `json:"max_supplier_concurrency"`
	FeeBPS                 *int                     `json:"fee_bps"`
	HoldHours              *int                     `json:"hold_hours"`
}

func (s *SupplierService) Options() SupplierOptions {
	o := SupplierOptions{ApplicationFields: []string{}, Providers: []SupplierProviderOption{}}
	if s.cfg != nil {
		c := s.cfg.Marketplace
		o.AgreementVersion = c.AgreementVersion
		o.AgreementText = c.AgreementText
		if c.AgreementURL == "" || validSupplierAgreement("", c.AgreementURL) {
			o.AgreementURL = c.AgreementURL
		}
		o.ApplicationFields = append(o.ApplicationFields, c.ApplicationFields...)
		o.MaxSupplierConcurrency = c.MaxSupplierConcurrency
		o.FeeBPS = c.FeeBPS
		o.HoldHours = c.HoldHours
	}
	return o
}
