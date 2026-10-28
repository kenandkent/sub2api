//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	appErr "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type supplierTestRepo struct {
	profile *SupplierProfile
	err     error
	owner   int64
}

func (r *supplierTestRepo) GetProfile(_ context.Context, id int64) (*SupplierProfile, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.profile == nil || r.profile.UserID != id {
		return nil, nil
	}
	return r.profile, nil
}
func (r *supplierTestRepo) Apply(_ context.Context, id, agreement int64, fields map[string]string) (*SupplierProfile, error) {
	r.profile = &SupplierProfile{UserID: id, Status: SupplierStatusPending, AgreementVersion: agreement, ApplicationFields: fields}
	return r.profile, nil
}
func (r *supplierTestRepo) Review(_ context.Context, user, admin int64, status SupplierStatus, reason string) (*SupplierProfile, error) {
	return nil, errors.New("unused")
}
func (r *supplierTestRepo) ListProfiles(context.Context, int, int) ([]SupplierProfile, int64, error) {
	return nil, 0, nil
}
func (r *supplierTestRepo) ListAccounts(_ context.Context, owner int64) ([]SupplierAccount, error) {
	if owner == r.owner {
		return []SupplierAccount{{ID: 7}}, nil
	}
	return []SupplierAccount{}, nil
}
func (r *supplierTestRepo) GetAccount(_ context.Context, owner, id int64) (*SupplierAccount, error) {
	if owner == r.owner && id == 7 {
		return &SupplierAccount{ID: id}, nil
	}
	return nil, appErr.NotFound("not_found", "not found")
}

func TestSupplierCapabilitiesAndStatus(t *testing.T) {
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, OnboardingEnabled: true, AgreementVersion: 3}}
	for _, tt := range []struct {
		status              SupplierStatus
		canApply, canManage bool
		want                string
	}{{"", true, false, "not_applied"}, {SupplierStatusPending, true, false, "pending"}, {SupplierStatusRejected, true, false, "rejected"}, {SupplierStatusActive, false, true, "active"}, {SupplierStatusSuspended, false, false, "suspended"}} {
		r := &supplierTestRepo{}
		if tt.status != "" {
			r.profile = &SupplierProfile{UserID: 42, Status: tt.status}
		}
		svc := NewSupplierService(r, cfg)
		got, err := svc.Capabilities(context.Background(), 42)
		if err != nil || !got.Consumer || got.SupplierStatus != tt.want || len(got.AllowedWorkspaces) != 2 {
			t.Fatalf("status %s: %+v %v", tt.status, got, err)
		}
		cap, err := svc.SupplierCapability(context.Background(), 42)
		if err != nil || cap.CanApply != tt.canApply || cap.CanManageOffers != tt.canManage {
			t.Fatalf("status %s: %+v %v", tt.status, cap, err)
		}
	}
}
func TestSupplierApplyAndOwnership(t *testing.T) {
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, OnboardingEnabled: true, AgreementVersion: 3, AgreementText: "Terms", ApplicationFields: []string{"contact"}}}
	r := &supplierTestRepo{owner: 42}
	svc := NewSupplierService(r, cfg)
	ctx := context.Background()
	if _, err := svc.Apply(ctx, 42, 2, map[string]string{"contact": "x"}); !appErr.IsBadRequest(err) {
		t.Fatalf("bad agreement: %v", err)
	}
	p, err := svc.Apply(ctx, 42, 3, map[string]string{"contact": "x"})
	if err != nil || p.UserID != 42 || p.Status != SupplierStatusPending {
		t.Fatalf("application %+v %v", p, err)
	}
	if _, err = svc.GetAccount(ctx, 43, 7); !appErr.IsNotFound(err) {
		t.Fatalf("other owner: %v", err)
	}
	r.profile.Status = SupplierStatusSuspended
	if _, err = svc.Apply(ctx, 42, 3, map[string]string{"contact": "x"}); !appErr.IsForbidden(err) {
		t.Fatalf("suspended application: %v", err)
	}
}
func TestSupplierDisabledPreservesHistory(t *testing.T) {
	r := &supplierTestRepo{profile: &SupplierProfile{UserID: 42, Status: SupplierStatusSuspended}}
	svc := NewSupplierService(r, &config.Config{})
	c, err := svc.Capabilities(context.Background(), 42)
	if err != nil || !c.Consumer || c.MarketplaceEnabled || len(c.AllowedWorkspaces) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := svc.Apply(context.Background(), 42, 1, nil); !appErr.IsForbidden(err) {
		t.Fatal(err)
	}
}

func TestSupplierOptionsAndSimpleMode(t *testing.T) {
	n, fee, hold := 4, 200, 24
	cfg := &config.Config{RunMode: config.RunModeSimple, Marketplace: config.MarketplaceConfig{Enabled: true, OnboardingEnabled: true, AgreementVersion: 7, ApplicationFields: []string{"contact"}, MaxSupplierConcurrency: &n, FeeBPS: &fee, HoldHours: &hold}}
	svc := NewSupplierService(&supplierTestRepo{}, cfg)
	options := svc.Options()
	if options.AgreementVersion != 7 || len(options.ApplicationFields) != 1 || *options.MaxSupplierConcurrency != 4 || *options.FeeBPS != 200 || *options.HoldHours != 24 {
		t.Fatalf("options: %+v", options)
	}
	caps, err := svc.Capabilities(context.Background(), 1)
	if err != nil || caps.MarketplaceEnabled || len(caps.AllowedWorkspaces) != 1 {
		t.Fatalf("simple mode: %+v %v", caps, err)
	}
	if _, err = svc.Apply(context.Background(), 1, 7, map[string]string{"contact": "x"}); !appErr.IsForbidden(err) {
		t.Fatalf("simple mode application: %v", err)
	}
}

func TestSupplierApplyRequiresVisibleAgreement(t *testing.T) {
	for _, tc := range []struct {
		text, url string
		ok        bool
	}{{"", "", false}, {"", "javascript:alert(1)", false}, {"", "//evil.example", false}, {"", "/%2f%2fevil.example", false}, {"", "/%5cevil.example", false}, {"", "http://example.com", false}, {"Terms", "", true}, {"", "https://example.com/terms", true}, {"", "/terms", true}} {
		cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, OnboardingEnabled: true, AgreementVersion: 1, AgreementText: tc.text, AgreementURL: tc.url}}
		svc := NewSupplierService(&supplierTestRepo{}, cfg)
		opts := svc.Options()
		if opts.AgreementText != tc.text || (tc.ok && opts.AgreementURL != tc.url) {
			t.Fatalf("options missing terms: %+v", opts)
		}
		_, err := svc.Apply(context.Background(), 1, 1, nil)
		if (err == nil) != tc.ok {
			t.Fatalf("text %q url %q: %v", tc.text, tc.url, err)
		}
	}
}

func TestSupplierApplicationRejectsSecretFieldNames(t *testing.T) {
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, OnboardingEnabled: true, AgreementVersion: 1, AgreementText: "Terms", ApplicationFields: []string{"api_key"}}}
	_, err := NewSupplierService(&supplierTestRepo{}, cfg).Apply(context.Background(), 1, 1, map[string]string{"api_key": "secret"})
	if !appErr.IsBadRequest(err) {
		t.Fatalf("secret field accepted: %v", err)
	}
}

func TestSupplierApplicationEmptyFieldsAreAnObject(t *testing.T) {
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{Enabled: true, OnboardingEnabled: true, AgreementVersion: 1, AgreementText: "Terms"}}
	p, err := NewSupplierService(&supplierTestRepo{}, cfg).Apply(context.Background(), 1, 1, nil)
	if err != nil || p.ApplicationFields == nil {
		t.Fatalf("profile fields must be an object: %+v %v", p, err)
	}
}

func TestSupplierAccountsRequireProfileAndOwnership(t *testing.T) {
	r := &supplierTestRepo{owner: 42}
	svc := NewSupplierService(r, &config.Config{})
	ctx := context.Background()
	if _, err := svc.GetAccount(ctx, 42, 7); !appErr.IsNotFound(err) {
		t.Fatalf("owner without profile: %v", err)
	}
	if _, err := svc.ListAccounts(ctx, 42); !appErr.IsForbidden(err) {
		t.Fatalf("list without profile: %v", err)
	}
	for _, status := range []SupplierStatus{SupplierStatusPending, SupplierStatusRejected, SupplierStatusActive, SupplierStatusSuspended} {
		r.profile = &SupplierProfile{UserID: 42, Status: status}
		a, err := svc.GetAccount(ctx, 42, 7)
		if err != nil || a.ID != 7 {
			t.Fatalf("%s account: %+v %v", status, a, err)
		}
		if _, err = svc.GetAccount(ctx, 43, 7); !appErr.IsNotFound(err) {
			t.Fatalf("%s other owner: %v", status, err)
		}
	}
}
