package service

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"strings"
	"testing"
)

type authorizationTestStore struct {
	sessions map[string]SupplierAuthorizationSession
}

func (s *authorizationTestStore) Put(_ context.Context, id string, v SupplierAuthorizationSession) error {
	s.sessions[id] = v
	return nil
}
func (s *authorizationTestStore) Consume(_ context.Context, id string, user int64) (SupplierAuthorizationSession, error) {
	v, ok := s.sessions[id]
	if !ok || v.UserID != user {
		return SupplierAuthorizationSession{}, ErrSupplierAuthorizationInvalid
	}
	delete(s.sessions, id)
	return v, nil
}

type authorizationTestExchange struct{}

func (authorizationTestExchange) ExchangeCode(_ context.Context, code, verifier, redirect, proxy, client string) (*openai.TokenResponse, error) {
	if code != "code" || verifier == "" || redirect != "https://example.test/callback" || proxy != "" || client == "" {
		return nil, ErrSupplierAuthorizationInvalid
	}
	return &openai.TokenResponse{AccessToken: "access-secret", RefreshToken: "refresh-secret"}, nil
}

type authorizationTestWriter struct{ calls int }

func (w *authorizationTestWriter) SaveSupplierAuthorization(_ context.Context, owner int64, purpose string, target int64, provider string, token *openai.TokenResponse) (int64, error) {
	w.calls++
	if owner != 7 || purpose != "create" || target != 0 || provider != "openai" || token.AccessToken != "access-secret" {
		return 0, ErrSupplierAuthorizationInvalid
	}
	return 42, nil
}
func TestSupplierAuthorizationBindingAndNoTokenResponse(t *testing.T) {
	ctx := context.Background()
	store := &authorizationTestStore{sessions: map[string]SupplierAuthorizationSession{}}
	writer := &authorizationTestWriter{}
	svc, err := NewSupplierAuthorizationService(store, authorizationTestExchange{}, writer, SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	start, err := svc.Start(ctx, 7, "create", 0)
	if err != nil {
		t.Fatal(err)
	}
	state := store.sessions[start.SessionID].State
	if _, err = svc.Complete(ctx, 8, start.SessionID, state, "code", "create", 0); err == nil {
		t.Fatal("wrong user accepted")
	}
	if _, err = svc.Complete(ctx, 7, start.SessionID, state, "code", "reauthorize", 42); err == nil {
		t.Fatal("binding mismatch accepted")
	}
	if _, err = svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0); err == nil {
		t.Fatal("replay accepted")
	}
	start, err = svc.Start(ctx, 7, "create", 0)
	if err != nil {
		t.Fatal(err)
	}
	state = store.sessions[start.SessionID].State
	result, err := svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0)
	if err != nil || result.AccountID != 42 {
		t.Fatalf("complete: %+v %v", result, err)
	}
	out, _ := json.Marshal(result)
	if strings.Contains(string(out), "secret") {
		t.Fatal("token returned to browser")
	}
	if writer.calls != 1 {
		t.Fatalf("unexpected writes %d", writer.calls)
	}
	if _, err = svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0); err == nil {
		t.Fatal("replay accepted")
	}
}

type authorizationExchangeMonitor struct{ calls int }

func (x *authorizationExchangeMonitor) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	x.calls++
	return &openai.TokenResponse{AccessToken: "access-secret"}, nil
}

func TestSupplierAuthorizationRejectsChangedSessionAndState(t *testing.T) {
	ctx := context.Background()
	for _, change := range []struct {
		name          string
		mutate        func(*SupplierAuthorizationSession)
		suppliedState string
	}{
		{"state", func(*SupplierAuthorizationSession) {}, "wrong-state"},
		{"provider", func(s *SupplierAuthorizationSession) { s.Provider = "other" }, ""},
		{"redirect", func(s *SupplierAuthorizationSession) { s.RedirectURI = "https://evil.test/callback" }, ""},
		{"proxy", func(s *SupplierAuthorizationSession) { s.ProxyURL = "https://evil.test" }, ""},
		{"client", func(s *SupplierAuthorizationSession) { s.ClientID = "other" }, ""},
	} {
		t.Run(change.name, func(t *testing.T) {
			store := &authorizationTestStore{sessions: map[string]SupplierAuthorizationSession{}}
			writer := &authorizationTestWriter{}
			exchange := &authorizationExchangeMonitor{}
			svc, err := NewSupplierAuthorizationService(store, exchange, writer, SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
			if err != nil {
				t.Fatal(err)
			}
			start, err := svc.Start(ctx, 7, "create", 0)
			if err != nil {
				t.Fatal(err)
			}
			session := store.sessions[start.SessionID]
			state := session.State
			change.mutate(&session)
			store.sessions[start.SessionID] = session
			if change.suppliedState != "" {
				state = change.suppliedState
			}
			if _, err = svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0); err == nil {
				t.Fatal("changed binding accepted")
			}
			if writer.calls != 0 || exchange.calls != 0 {
				t.Fatal("invalid session reached exchange or persistence")
			}
		})
	}
}
