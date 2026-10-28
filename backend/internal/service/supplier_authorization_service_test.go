package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"net/url"
	"strings"
	"sync"
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

type authorizationTestOperations struct {
	mu     sync.Mutex
	values map[string]SupplierAuthorizationOperation
}

func (s *authorizationTestOperations) Create(_ context.Context, op SupplierAuthorizationOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[op.ID] = op
	return nil
}
func (s *authorizationTestOperations) Get(_ context.Context, user int64, id string) (*SupplierAuthorizationOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.values[id]
	if !ok || op.UserID != user {
		return nil, nil
	}
	return &op, nil
}
func (s *authorizationTestOperations) Claim(_ context.Context, user int64, id, digest string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.values[id]
	if !ok || op.UserID != user || op.Status != "pending" {
		return false, nil
	}
	op.Status = "processing"
	op.CallbackDigest = digest
	s.values[id] = op
	return true, nil
}
func (s *authorizationTestOperations) Fail(_ context.Context, user int64, id, expected, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.values[id]
	if op.UserID == user && op.Status == expected {
		op.Status = "failed"
		if code == "write_uncertain" {
			op.Status = "needs_review"
		}
		s.values[id] = op
	}
	return nil
}

type authorizationAllow struct{}

func (authorizationAllow) AuthorizeSupplierAuthorization(context.Context, int64, string, int64) error {
	return nil
}

type authorizationTestWriter struct {
	calls int
	ops   *authorizationTestOperations
}

func (w *authorizationTestWriter) SaveSupplierAuthorization(ctx context.Context, owner int64, purpose string, target int64, provider string, token *openai.TokenResponse) (int64, error) {
	w.calls++
	if owner != 7 || purpose != "create" || target != 0 || provider != "openai" || token.AccessToken != "access-secret" {
		return 0, ErrSupplierAuthorizationInvalid
	}
	if w.ops != nil {
		id := SupplierAuthorizationOperationID(ctx)
		w.ops.mu.Lock()
		op := w.ops.values[id]
		account := int64(42)
		op.Status = "completed"
		op.AccountID = &account
		w.ops.values[id] = op
		w.ops.mu.Unlock()
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
	ops := &authorizationTestOperations{values: map[string]SupplierAuthorizationOperation{}}
	writer.ops = ops
	svc.SetOperationStore(ops)
	svc.SetAuthorizer(authorizationAllow{})
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
	if result, err := svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0); err != nil || result.AccountID != 42 {
		t.Fatalf("valid callback after mismatches: %v", err)
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
	if writer.calls != 2 {
		t.Fatalf("unexpected writes %d", writer.calls)
	}
	if result, err = svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0); err != nil || result.AccountID != 42 {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if _, err = svc.Complete(ctx, 7, start.SessionID, state, "other-code", "create", 0); err == nil {
		t.Fatal("changed code accepted")
	}
	if writer.calls != 2 {
		t.Fatal("retry wrote again")
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
			ops := &authorizationTestOperations{values: map[string]SupplierAuthorizationOperation{}}
			writer.ops = ops
			svc.SetOperationStore(ops)
			svc.SetAuthorizer(authorizationAllow{})
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

func TestSupplierAuthorizationRequiresDurableDependencies(t *testing.T) {
	store := &authorizationTestStore{sessions: map[string]SupplierAuthorizationSession{}}
	svc, err := NewSupplierAuthorizationService(store, authorizationTestExchange{}, &authorizationTestWriter{}, SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(context.Background(), 7, "create", 0); err == nil {
		t.Fatal("start succeeded without durable store and authorizer")
	}
}

func TestSupplierAuthorizationPKCEURL(t *testing.T) {
	store := &authorizationTestStore{sessions: map[string]SupplierAuthorizationSession{}}
	svc, _ := NewSupplierAuthorizationService(store, authorizationTestExchange{}, &authorizationTestWriter{}, SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
	svc.SetOperationStore(&authorizationTestOperations{values: map[string]SupplierAuthorizationOperation{}})
	svc.SetAuthorizer(authorizationAllow{})
	start, err := svc.Start(context.Background(), 7, "create", 0)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	session := store.sessions[start.SessionID]
	if q.Get("state") != session.State || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != openai.GenerateCodeChallenge(session.CodeVerifier) || strings.Contains(start.AuthURL, session.CodeVerifier) {
		t.Fatal("invalid PKCE authorization URL")
	}
}

type authorizationDeny struct{}

func (authorizationDeny) AuthorizeSupplierAuthorization(context.Context, int64, string, int64) error {
	return errors.New("denied")
}

type authorizationUncertainWriter struct {
	ops       *authorizationTestOperations
	committed bool
}

func (w authorizationUncertainWriter) SaveSupplierAuthorization(ctx context.Context, user int64, purpose string, target int64, provider string, token *openai.TokenResponse) (int64, error) {
	if w.committed {
		w.ops.mu.Lock()
		id := SupplierAuthorizationOperationID(ctx)
		op := w.ops.values[id]
		account := int64(42)
		op.Status = "completed"
		op.AccountID = &account
		w.ops.values[id] = op
		w.ops.mu.Unlock()
	}
	return 0, errors.New("connection lost")
}
func TestSupplierAuthorizationUncertainWriteIsResolvedFromOperation(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			ctx := context.Background()
			cache := &authorizationTestStore{sessions: map[string]SupplierAuthorizationSession{}}
			ops := &authorizationTestOperations{values: map[string]SupplierAuthorizationOperation{}}
			svc, _ := NewSupplierAuthorizationService(cache, authorizationTestExchange{}, authorizationUncertainWriter{ops, committed}, SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
			svc.SetOperationStore(ops)
			svc.SetAuthorizer(authorizationAllow{})
			start, err := svc.Start(ctx, 7, "create", 0)
			if err != nil {
				t.Fatal(err)
			}
			state := cache.sessions[start.SessionID].State
			result, err := svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0)
			if committed {
				if err != nil || result.AccountID != 42 {
					t.Fatalf("committed result lost: %v", err)
				}
			} else if !errors.Is(err, ErrSupplierAuthorizationNeedsReview) {
				t.Fatalf("expected review: %v", err)
			}
			op, err := svc.Status(ctx, 7, start.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if committed && op.Status != "completed" || !committed && op.Status != "needs_review" {
				t.Fatalf("status %s", op.Status)
			}
			if _, err = svc.Status(ctx, 8, start.SessionID); !errors.Is(err, ErrSupplierAuthorizationInvalid) {
				t.Fatal("cross-owner status exposed")
			}
		})
	}
}
func TestSupplierAuthorizationChecksEligibilityBeforeExchange(t *testing.T) {
	ctx := context.Background()
	cache := &authorizationTestStore{sessions: map[string]SupplierAuthorizationSession{}}
	ops := &authorizationTestOperations{values: map[string]SupplierAuthorizationOperation{}}
	exchange := &authorizationExchangeMonitor{}
	svc, _ := NewSupplierAuthorizationService(cache, exchange, &authorizationTestWriter{ops: ops}, SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
	svc.SetOperationStore(ops)
	svc.SetAuthorizer(authorizationAllow{})
	start, err := svc.Start(ctx, 7, "create", 0)
	if err != nil {
		t.Fatal(err)
	}
	state := cache.sessions[start.SessionID].State
	svc.SetAuthorizer(authorizationDeny{})
	if _, err = svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0); err == nil {
		t.Fatal("suspended supplier accepted")
	}
	if exchange.calls != 0 {
		t.Fatal("exchange reached")
	}
	if _, err = svc.Start(ctx, 7, "create", 0); err == nil {
		t.Fatal("suspended supplier started authorization")
	}
}

func TestSupplierAuthorizationProcessingRejectsDifferentCallback(t *testing.T) {
	ctx := context.Background()
	cache := &authorizationTestStore{sessions: map[string]SupplierAuthorizationSession{}}
	ops := &authorizationTestOperations{values: map[string]SupplierAuthorizationOperation{}}
	exchange := &authorizationExchangeMonitor{}
	svc, _ := NewSupplierAuthorizationService(cache, exchange, &authorizationTestWriter{ops: ops}, SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
	svc.SetOperationStore(ops)
	svc.SetAuthorizer(authorizationAllow{})
	start, err := svc.Start(ctx, 7, "create", 0)
	if err != nil {
		t.Fatal(err)
	}
	state := cache.sessions[start.SessionID].State
	claimed, err := ops.Claim(ctx, 7, start.SessionID, authorizationDigest(state, "code", "create", int64(0)))
	if err != nil || !claimed {
		t.Fatal("claim failed")
	}
	if _, err = svc.Complete(ctx, 7, start.SessionID, state, "code", "create", 0); !errors.Is(err, ErrSupplierAuthorizationInProgress) {
		t.Fatalf("expected processing: %v", err)
	}
	if _, err = svc.Complete(ctx, 7, start.SessionID, state, "different", "create", 0); !errors.Is(err, ErrSupplierAuthorizationInvalid) {
		t.Fatalf("changed code accepted: %v", err)
	}
	if exchange.calls != 0 {
		t.Fatal("processing callback exchanged again")
	}
}
