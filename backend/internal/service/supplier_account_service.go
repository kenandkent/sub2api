package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

func validateSupplierProvider(p config.MarketplaceProviderConfig) error {
	if p.Platform != "openai" || strings.TrimSpace(p.Label) == "" || len(p.Label) > 100 || len(p.AccountTypes) == 0 || len(p.Models) == 0 || len(p.Models) > 200 || (p.ProxyID != nil && *p.ProxyID <= 0) {
		return errors.New("invalid marketplace provider")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid marketplace provider URL")
	}
	if _, err = urlvalidator.ValidateHTTPSURL(p.BaseURL, urlvalidator.ValidationOptions{}); err != nil {
		return errors.New("invalid marketplace provider URL")
	}
	seen := map[string]bool{}
	for _, v := range p.AccountTypes {
		if (v != "apikey" && v != "oauth") || seen[v] {
			return errors.New("invalid marketplace account type")
		}
		seen[v] = true
	}
	seen = map[string]bool{}
	for _, v := range p.Models {
		if strings.TrimSpace(v) != v || v == "" || len(v) > 200 || seen[v] {
			return errors.New("invalid marketplace model")
		}
		seen[v] = true
	}
	return nil
}

func validSupplierAPIKey(key string) bool {
	if len(key) < 24 || len(key) > 4096 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

func validSupplierModels(p config.MarketplaceProviderConfig, models []string) bool {
	if len(models) == 0 || len(models) > 200 {
		return false
	}
	allowed := make(map[string]bool, len(p.Models))
	for _, v := range p.Models {
		allowed[v] = true
	}
	seen := map[string]bool{}
	for _, v := range models {
		if !allowed[v] || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// SupplierAccountDetail is a deliberately credential-free account projection.
type SupplierAccountDetail struct {
	ID                 int64                   `json:"id"`
	Name               string                  `json:"name"`
	Platform           string                  `json:"platform"`
	Type               string                  `json:"type"`
	Status             string                  `json:"status"`
	Concurrency        int                     `json:"concurrency"`
	CurrentConcurrency int                     `json:"current_concurrency"`
	CredentialStatus   string                  `json:"credential_status"`
	Offer              SupplierOffer           `json:"offer"`
	Usage              SupplierAccountUsage    `json:"usage"`
	Reviews            []SupplierAccountReview `json:"reviews"`
}
type SupplierOffer struct {
	ID            int64    `json:"id"`
	ReviewStatus  string   `json:"review_status"`
	Enabled       bool     `json:"enabled"`
	AllowedModels []string `json:"allowed_models"`
	Version       int64    `json:"version"`
}
type SupplierAccountCreate struct {
	Name          string   `json:"name"`
	Platform      string   `json:"platform"`
	Type          string   `json:"type"`
	APIKey        string   `json:"api_key"`
	AllowedModels []string `json:"allowed_models"`
	Concurrency   int      `json:"concurrency"`
}
type SupplierAccountPatch struct {
	ExpectedVersion int64     `json:"expected_version"`
	Name            *string   `json:"name"`
	Concurrency     *int      `json:"concurrency"`
	AllowedModels   *[]string `json:"allowed_models"`
}
type SupplierOperation struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	AccountID *int64 `json:"account_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}
type SupplierAccountStore interface {
	Create(context.Context, int64, string, string, SupplierAccountCreate, config.MarketplaceProviderConfig) (*SupplierOperation, error)
	Get(context.Context, int64, int64) (*SupplierAccountDetail, error)
	Patch(context.Context, int64, int64, SupplierAccountPatch) (*SupplierAccountDetail, error)
	Transition(context.Context, int64, int64, int64, string) (*SupplierAccountDetail, error)
	Operation(context.Context, int64, string) (*SupplierOperation, error)
}
type SupplierAccountService struct {
	cfg         *config.Config
	store       SupplierAccountStore
	profiles    SupplierRepository
	catalog     SupplierCatalog
	concurrency *ConcurrencyService
}

func NewSupplierAccountService(cfg *config.Config, store SupplierAccountStore, profiles SupplierRepository, catalog SupplierCatalog, concurrency *ConcurrencyService) *SupplierAccountService {
	return &SupplierAccountService{cfg: cfg, store: store, profiles: profiles, catalog: catalog, concurrency: concurrency}
}
func (s *SupplierAccountService) authorize(ctx context.Context, user int64, write bool) error {
	if user <= 0 || s == nil || s.store == nil || s.profiles == nil {
		return apperrors.Forbidden("supplier_required", "Supplier profile required")
	}
	p, err := s.profiles.GetProfile(ctx, user)
	if err != nil {
		return err
	}
	if p == nil {
		return apperrors.Forbidden("supplier_required", "Supplier profile required")
	}
	if write && (s.cfg == nil || !s.cfg.Marketplace.Enabled || config.NormalizeRunMode(s.cfg.RunMode) != config.RunModeStandard || p.Status != SupplierStatusActive) {
		return apperrors.Forbidden("supplier_not_active", "Supplier cannot manage offers")
	}
	return nil
}
func (s *SupplierAccountService) provider(platform string) (config.MarketplaceProviderConfig, bool) {
	if s.cfg == nil {
		return config.MarketplaceProviderConfig{}, false
	}
	for _, p := range s.cfg.Marketplace.Providers {
		if p.Platform == platform && validateSupplierProvider(p) == nil {
			return p, true
		}
	}
	return config.MarketplaceProviderConfig{}, false
}

func supplierProviderAllowsType(p config.MarketplaceProviderConfig, accountType string) bool {
	for _, allowed := range p.AccountTypes {if allowed == accountType {return true}}
	return false
}

// AuthorizeSupplierAuthorization is checked both before creating the PKCE
// session and before consuming its callback. Targets are resolved by owner.
func (s *SupplierAccountService) AuthorizeSupplierAuthorization(ctx context.Context, user int64, purpose string, target int64) error {
	if err := s.authorize(ctx,user,true); err!=nil {return err}
	provider,ok:=s.provider(PlatformOpenAI)
	if !ok || !supplierProviderAllowsType(provider,AccountTypeOAuth) {return ErrSupplierAuthorizationInvalid}
	switch purpose {
	case "create":
		if target!=0 {return ErrSupplierAuthorizationInvalid}
	case "reauthorize":
		if target<=0 {return ErrSupplierAuthorizationInvalid}
		account,err:=s.store.Get(ctx,user,target)
		if err!=nil || account==nil || account.Platform!=PlatformOpenAI || account.Type!=AccountTypeOAuth {return ErrSupplierAuthorizationInvalid}
	default:return ErrSupplierAuthorizationInvalid
	}
	return nil
}
func (s *SupplierAccountService) Create(ctx context.Context, user int64, key string, in SupplierAccountCreate) (*SupplierOperation, error) {
	if in.Type=="api_key" {in.Type="apikey"}
	if err := s.authorize(ctx, user, true); err != nil {
		return nil, err
	}
	p, ok := s.provider(in.Platform)
	if !ok || in.Type != "apikey" || !supplierProviderAllowsType(p,in.Type) || (in.APIKey != "" && !validSupplierAPIKey(in.APIKey)) || strings.TrimSpace(in.Name) != in.Name || in.Name == "" || len(in.Name) > 100 || !validSupplierModels(p, in.AllowedModels) || s.cfg.Marketplace.MaxSupplierConcurrency == nil || in.Concurrency <= 0 || in.Concurrency > *s.cfg.Marketplace.MaxSupplierConcurrency || strings.TrimSpace(key) != key || len(key) == 0 || len(key) > 200 {
		return nil, apperrors.BadRequest("invalid_account", "Invalid account or idempotency key")
	}
	// Only a digest of the key enters durable operation metadata.
	hashed := sha256.Sum256([]byte(in.APIKey))
	digestInput, _ := json.Marshal(struct {
		Name, Platform, Type, KeyDigest string
		Models                          []string
		Concurrency                     int
	}{in.Name, in.Platform, in.Type, hex.EncodeToString(hashed[:]), in.AllowedModels, in.Concurrency})
	digest := sha256.Sum256(digestInput)
	return s.store.Create(ctx, user, key, hex.EncodeToString(digest[:]), in, p)
}
func (s *SupplierAccountService) Get(ctx context.Context, user, id int64) (*SupplierAccountDetail, error) {
	if err := s.authorize(ctx, user, false); err != nil {
		return nil, err
	}
	a, err := s.store.Get(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if s.concurrency == nil {
		return nil, apperrors.ServiceUnavailable("concurrency_unavailable", "Account concurrency unavailable")
	}
	{
		m, e := s.concurrency.GetAccountConcurrencyBatch(ctx, []int64{id})
		if e != nil {
			return nil, e
		}
		a.CurrentConcurrency = m[id]
	}
	return a, nil
}
func (s *SupplierAccountService) Patch(ctx context.Context, user, id int64, in SupplierAccountPatch) (*SupplierAccountDetail, error) {
	if err := s.authorize(ctx, user, true); err != nil {
		return nil, err
	}
	a, err := s.store.Get(ctx, user, id)
	if err != nil {
		return nil, err
	}
	p, ok := s.provider(a.Platform)
	if !ok || in.ExpectedVersion <= 0 || (in.Name != nil && (strings.TrimSpace(*in.Name) != *in.Name || *in.Name == "" || len(*in.Name) > 100)) || (in.Concurrency != nil && (*in.Concurrency <= 0 || s.cfg.Marketplace.MaxSupplierConcurrency == nil || *in.Concurrency > *s.cfg.Marketplace.MaxSupplierConcurrency)) || (in.AllowedModels != nil && !validSupplierModels(p, *in.AllowedModels)) {
		return nil, apperrors.BadRequest("invalid_account", "Invalid account update")
	}
	return s.store.Patch(ctx, user, id, in)
}
func (s *SupplierAccountService) Transition(ctx context.Context, user, id, version int64, action string) (*SupplierAccountDetail, error) {
	if err := s.authorize(ctx, user, true); err != nil {
		return nil, err
	}
	if version <= 0 {
		return nil, apperrors.BadRequest("invalid_version", "Expected version required")
	}
	a, err := s.store.Get(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if action == "submit" {
		if a.CredentialStatus != "valid" && a.CredentialStatus != "configured" {
			return nil, apperrors.Conflict("credentials_missing", "Credential required")
		}
		if s.catalog == nil {
			return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
		}
		prices, e := s.catalog.Prices(ctx, a.Platform, a.Offer.AllowedModels)
		if e != nil {
			return nil, e
		}
		covered := map[string]bool{}
		for _, p := range prices {
			covered[p.Model] = true
		}
		for _, m := range a.Offer.AllowedModels {
			if !covered[m] {
				return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
			}
		}
		if len(a.Offer.AllowedModels) == 0 {
			return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
		}
	}
	if action != "submit" && action != "pause" && action != "resume" && action != "revoke" {
		return nil, apperrors.BadRequest("invalid_action", "Invalid action")
	}
	return s.store.Transition(ctx, user, id, version, action)
}
func (s *SupplierAccountService) Operation(ctx context.Context, user int64, key string) (*SupplierOperation, error) {
	if err := s.authorize(ctx, user, false); err != nil {
		return nil, err
	}
	if len(key) == 0 || len(key) > 200 {
		return nil, apperrors.NotFound("operation_not_found", "Operation not found")
	}
	return s.store.Operation(ctx, user, key)
}

type SupplierAccountFilter struct {
	Query    string
	Platform string
	Status   string
	Model    string
	Page     int
	PageSize int
}
type SupplierAccountPage struct {
	Items    []SupplierAccountDetail `json:"items"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
	Pages    int                     `json:"pages"`
}
type SupplierAccountListStore interface {
	List(context.Context, int64, SupplierAccountFilter) ([]SupplierAccountDetail, int64, error)
}

func (s *SupplierAccountService) List(ctx context.Context, user int64, f SupplierAccountFilter) (*SupplierAccountPage, error) {
	if err := s.authorize(ctx, user, false); err != nil {
		return nil, err
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 20
	}
	if f.Page > 1000000 {
		return nil, apperrors.BadRequest("invalid_filter", "Invalid page")
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}
	if len(f.Query) > 100 || len(f.Platform) > 50 || len(f.Status) > 20 || len(f.Model) > 200 {
		return nil, apperrors.BadRequest("invalid_filter", "Invalid filter")
	}
	store, ok := s.store.(SupplierAccountListStore)
	if !ok {
		return nil, apperrors.Conflict("accounts_unavailable", "Accounts unavailable")
	}
	items, total, err := store.List(ctx, user, f)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(items))
	for _, a := range items {
		ids = append(ids, a.ID)
	}
	if len(ids) > 0 && s.concurrency == nil {
		return nil, apperrors.ServiceUnavailable("concurrency_unavailable", "Account concurrency unavailable")
	}
	if len(ids) > 0 {
		loads, e := s.concurrency.GetAccountConcurrencyBatch(ctx, ids)
		if e != nil {
			return nil, e
		}
		for i := range items {
			items[i].CurrentConcurrency = loads[items[i].ID]
		}
	}
	return &SupplierAccountPage{Items: items, Total: total, Page: f.Page, PageSize: f.PageSize, Pages: int((total + int64(f.PageSize) - 1) / int64(f.PageSize))}, nil
}

type SupplierAccountReviewStore interface {
	GetForReview(context.Context, int64) (*SupplierAccountDetail, error)
	Review(context.Context, int64, int64, int64, string, string, []int64) (*SupplierAccountDetail, error)
}

func (s *SupplierAccountService) AdminReview(ctx context.Context, admin, id, version int64, status, reason string) (*SupplierAccountDetail, error) {
	reason = strings.TrimSpace(reason)
	if admin <= 0 || id <= 0 || version <= 0 || len(reason) == 0 || len(reason) > 1000 || (status != "approved" && status != "rejected") {
		return nil, apperrors.BadRequest("invalid_review", "Valid status and reason required")
	}
	if s == nil || s.store == nil || s.cfg == nil || !s.cfg.Marketplace.Enabled || config.NormalizeRunMode(s.cfg.RunMode) != config.RunModeStandard {
		return nil, apperrors.Forbidden("marketplace_disabled", "Marketplace unavailable")
	}
	store, ok := s.store.(SupplierAccountReviewStore)
	if !ok {
		return nil, apperrors.Conflict("review_unavailable", "Review unavailable")
	}
	a, err := store.GetForReview(ctx, id)
	if err != nil {
		return nil, err
	}
	groups := []int64{}
	if status == "approved" {
		if s.catalog == nil {
			return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
		}
		prices, e := s.catalog.Prices(ctx, a.Platform, a.Offer.AllowedModels)
		if e != nil {
			return nil, e
		}
		covered := map[string]bool{}
		allowed := map[string]bool{}
		for _, model := range a.Offer.AllowedModels {
			allowed[model] = true
		}
		seen := map[int64]bool{}
		for _, p := range prices {
			if p.GroupID <= 0 || !allowed[p.Model] {
				continue
			}
			covered[p.Model] = true
			if !seen[p.GroupID] {
				seen[p.GroupID] = true
				groups = append(groups, p.GroupID)
			}
		}
		for _, m := range a.Offer.AllowedModels {
			if !covered[m] {
				return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
			}
		}
		if len(groups) == 0 || len(a.Offer.AllowedModels) == 0 {
			return nil, apperrors.Conflict("price_unavailable", "Marketplace prices unavailable")
		}
	}
	return store.Review(ctx, admin, id, version, status, reason, groups)
}

type SupplierCredentialStore interface {
	ReplaceCredentials(context.Context, int64, int64, string, string, string, int64) (*SupplierOperation, error)
}

func (s *SupplierAccountService) ReplaceCredentials(ctx context.Context, user, id int64, key, apiKey string, version int64) (*SupplierOperation, error) {
	if err := s.authorize(ctx, user, true); err != nil {
		return nil, err
	}
	if id <= 0 || version <= 0 || len(key) == 0 || len(key) > 200 || strings.TrimSpace(key) != key || !validSupplierAPIKey(apiKey) {
		return nil, apperrors.BadRequest("invalid_credentials", "Invalid credential or idempotency key")
	}
	a, err := s.store.Get(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if a.Type != "apikey" {
		return nil, apperrors.BadRequest("invalid_credentials", "Account type requires authorization")
	}
	if _, ok := s.provider(a.Platform); !ok {
		return nil, apperrors.Conflict("provider_unavailable", "Provider unavailable")
	}
	digest := sha256.Sum256([]byte(apiKey))
	body, _ := json.Marshal(struct {
		Account   int64
		Version   int64
		KeyDigest string
	}{id, version, hex.EncodeToString(digest[:])})
	d := sha256.Sum256(body)
	store, ok := s.store.(SupplierCredentialStore)
	if !ok {
		return nil, apperrors.Conflict("credentials_unavailable", "Credentials unavailable")
	}
	return store.ReplaceCredentials(ctx, user, id, key, hex.EncodeToString(d[:]), apiKey, version)
}

type SupplierProviderOption struct {
	Platform     string               `json:"platform"`
	Label        string               `json:"label"`
	AccountTypes []string             `json:"account_types"`
	Models       []string             `json:"models"`
	Prices       []SupplierModelPrice `json:"prices"`
}

func (s *SupplierAccountService) Providers(ctx context.Context) ([]SupplierProviderOption, error) {
	out := []SupplierProviderOption{}
	if s == nil || s.cfg == nil {
		return out, nil
	}
	for _, p := range s.cfg.Marketplace.Providers {
		if validateSupplierProvider(p) != nil {
			continue
		}
		prices := []SupplierModelPrice{}
		if s.catalog != nil {
			var err error
			prices, err = s.catalog.Prices(ctx, p.Platform, p.Models)
			if err != nil {
				return nil, err
			}
			if prices == nil {
				prices = []SupplierModelPrice{}
			}
		}
		out = append(out, SupplierProviderOption{Platform: p.Platform, Label: p.Label, AccountTypes: append([]string(nil), p.AccountTypes...), Models: append([]string(nil), p.Models...), Prices: prices})
	}
	return out, nil
}

type SupplierAccountUsage struct {
	Requests       int64  `json:"requests"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	ConsumerCharge string `json:"consumer_charge"`
	SupplierNet    string `json:"supplier_net"`
}
type SupplierAccountReview struct {
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}
type SupplierDailyUsage struct {
	Date string `json:"date"`
	SupplierAccountUsage
}
type SupplierOverview struct {
	Stats      SupplierAccountUsage `json:"stats"`
	DailyUsage []SupplierDailyUsage `json:"daily_usage"`
	UpdatedAt  time.Time            `json:"updated_at"`
}
type SupplierOverviewStore interface {
	Overview(context.Context, int64) (*SupplierOverview, error)
}

func (s *SupplierAccountService) Overview(ctx context.Context, user int64) (*SupplierOverview, error) {
	if err := s.authorize(ctx, user, false); err != nil {
		return nil, err
	}
	store, ok := s.store.(SupplierOverviewStore)
	if !ok {
		return nil, apperrors.Conflict("overview_unavailable", "Overview unavailable")
	}
	return store.Overview(ctx, user)
}

// AdminSupplierOffer contains only metadata suitable for an administrator review queue.
type AdminSupplierOffer struct {
	ID int64 `json:"id"`
	OfferID int64 `json:"offer_id"`
	AccountID int64 `json:"account_id"`
	OwnerUserID int64 `json:"owner_user_id"`
	AccountName string `json:"account_name"`
	Platform string `json:"platform"`
	Type string `json:"type"`
	AccountStatus string `json:"account_status"`
	SupplierStatus string `json:"supplier_status"`
	IdentityVerified bool `json:"identity_verified"`
	Concurrency int `json:"concurrency"`
	ReviewStatus string `json:"review_status"`
	Enabled bool `json:"enabled"`
	AllowedModels []string `json:"allowed_models"`
	Version int64 `json:"version"`
}
type AdminSupplierOfferPage struct {
	Items []AdminSupplierOffer `json:"items"`
	Total int64 `json:"total"`
	Page int `json:"page"`
	PageSize int `json:"page_size"`
	Pages int `json:"pages"`
}
type supplierAdminOfferListStore interface {
	ListAdminOffers(context.Context, SupplierAccountFilter) ([]AdminSupplierOffer, int64, error)
}
func (s *SupplierAccountService) AdminListOffers(ctx context.Context, admin int64, f SupplierAccountFilter) (*AdminSupplierOfferPage, error) {
	if admin <= 0 { return nil, apperrors.Forbidden("administrator_required", "Administrator required") }
	if f.Page < 1 { f.Page = 1 }; if f.PageSize < 1 { f.PageSize = 20 }; if f.PageSize > 100 { f.PageSize = 100 }
	if f.Page > 1000000 || len(f.Query) > 100 || len(f.Platform) > 50 || len(f.Status) > 20 { return nil, apperrors.BadRequest("invalid_filter", "Invalid filter") }
	store, ok := s.store.(supplierAdminOfferListStore); if !ok { return nil, apperrors.Conflict("offers_unavailable", "Offers unavailable") }
	items, total, err := store.ListAdminOffers(ctx, f); if err != nil { return nil, err }
	return &AdminSupplierOfferPage{Items: items, Total: total, Page: f.Page, PageSize: f.PageSize, Pages: int((total+int64(f.PageSize)-1)/int64(f.PageSize))}, nil
}
