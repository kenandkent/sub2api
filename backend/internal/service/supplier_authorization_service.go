package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

var ErrSupplierAuthorizationInvalid = errors.New("supplier authorization invalid or expired")
var ErrSupplierAuthorizationFailed = errors.New("supplier authorization failed")
var ErrSupplierAuthorizationUnavailable = errors.New("supplier authorization temporarily unavailable")
var ErrSupplierAuthorizationInProgress = errors.New("supplier authorization in progress")
var ErrSupplierAuthorizationNeedsReview = errors.New("supplier authorization needs review")

type SupplierAuthorizationOperation struct {
	UserID         int64  `json:"-"`
	ID             string `json:"-"`
	Purpose        string `json:"-"`
	Target         int64  `json:"-"`
	Provider       string `json:"-"`
	StateDigest    string `json:"-"`
	CallbackDigest string `json:"-"`
	Status         string `json:"status"`
	AccountID      *int64 `json:"account_id,omitempty"`
}
type SupplierAuthorizationOperationStore interface {
	Create(context.Context, SupplierAuthorizationOperation) error
	Get(context.Context, int64, string) (*SupplierAuthorizationOperation, error)
	Claim(context.Context, int64, string, string) (bool, error)
	Fail(context.Context, int64, string, string, string) error
}
type SupplierAuthorizationAuthorizer interface {
	AuthorizeSupplierAuthorization(context.Context, int64, string, int64) error
}

func authorizationDigest(values ...any) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SupplierAuthorizationSession is confidential server-side state; never return it to clients.
type SupplierAuthorizationSession struct {
	UserID       int64  `json:"user_id"`
	Purpose      string `json:"purpose"`
	AccountID    int64  `json:"account_id"`
	Provider     string `json:"provider"`
	RedirectURI  string `json:"redirect_uri"`
	ProxyURL     string `json:"proxy_url"`
	ClientID     string `json:"client_id"`
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
}
type SupplierAuthorizationStore interface {
	Put(context.Context, string, SupplierAuthorizationSession) error
	Consume(context.Context, string, int64) (SupplierAuthorizationSession, error)
}

// SupplierOpenAIExchanger is implemented by the existing OpenAIOAuthClient.
type SupplierOpenAIExchanger interface {
	ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error)
}

// SupplierAuthorizationWriter must persist only encrypted credentials transactionally and owner-scope updates.
type SupplierAuthorizationWriter interface {
	SaveSupplierAuthorization(context.Context, int64, string, int64, string, *openai.TokenResponse) (int64, error)
}
type SupplierAuthorizationConfig struct {
	Provider    string
	RedirectURI string
	ProxyURL    string
}
type SupplierAuthorizationService struct {
	store      SupplierAuthorizationStore
	exchange   SupplierOpenAIExchanger
	writer     SupplierAuthorizationWriter
	config     SupplierAuthorizationConfig
	operations SupplierAuthorizationOperationStore
	authorizer SupplierAuthorizationAuthorizer
}
type SupplierAuthorizationStart struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
}
type SupplierAuthorizationResult struct {
	AccountID int64 `json:"account_id"`
}

func NewSupplierAuthorizationService(store SupplierAuthorizationStore, exchange SupplierOpenAIExchanger, writer SupplierAuthorizationWriter, cfg SupplierAuthorizationConfig) (*SupplierAuthorizationService, error) {
	u, err := url.Parse(cfg.RedirectURI)
	if store == nil || exchange == nil || writer == nil || cfg.Provider != "openai" || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, ErrSupplierAuthorizationInvalid
	}
	return &SupplierAuthorizationService{store: store, exchange: exchange, writer: writer, config: cfg}, nil
}

// SetOperationStore and SetAuthorizer must both be configured before use.
func (s *SupplierAuthorizationService) SetOperationStore(store SupplierAuthorizationOperationStore) {
	s.operations = store
}
func (s *SupplierAuthorizationService) SetAuthorizer(authorizer SupplierAuthorizationAuthorizer) {
	s.authorizer = authorizer
}
func (s *SupplierAuthorizationService) Status(ctx context.Context, user int64, id string) (*SupplierAuthorizationOperation, error) {
	if s.operations == nil || user <= 0 || id == "" {
		return nil, ErrSupplierAuthorizationInvalid
	}
	op, err := s.operations.Get(ctx, user, id)
	if err != nil {
		return nil, ErrSupplierAuthorizationUnavailable
	}
	if op == nil {
		return nil, ErrSupplierAuthorizationInvalid
	}
	return op, nil
}
func validSupplierPurpose(purpose string, target int64) bool {
	return (purpose == "create" && target == 0) || (purpose == "reauthorize" && target > 0)
}
func (s *SupplierAuthorizationService) Start(ctx context.Context, user int64, purpose string, target int64) (*SupplierAuthorizationStart, error) {
	if s.operations == nil || s.authorizer == nil || user <= 0 || !validSupplierPurpose(purpose, target) {
		return nil, ErrSupplierAuthorizationInvalid
	}
	if err := s.authorizer.AuthorizeSupplierAuthorization(ctx, user, purpose, target); err != nil {
		return nil, err
	}
	state, err := openai.GenerateState()
	if err != nil {
		return nil, ErrSupplierAuthorizationFailed
	}
	verifier, err := openai.GenerateCodeVerifier()
	if err != nil {
		return nil, ErrSupplierAuthorizationFailed
	}
	id, err := openai.GenerateSessionID()
	if err != nil {
		return nil, ErrSupplierAuthorizationFailed
	}
	clientID, _ := openai.OAuthClientConfigByPlatform(openai.OAuthPlatformOpenAI)
	session := SupplierAuthorizationSession{UserID: user, Purpose: purpose, AccountID: target, Provider: s.config.Provider, RedirectURI: s.config.RedirectURI, ProxyURL: s.config.ProxyURL, ClientID: clientID, State: state, CodeVerifier: verifier}
	if err = s.operations.Create(ctx, SupplierAuthorizationOperation{UserID: user, ID: id, Purpose: purpose, Target: target, Provider: s.config.Provider, StateDigest: authorizationDigest(state), Status: "pending"}); err != nil {
		return nil, ErrSupplierAuthorizationUnavailable
	}
	if err = s.store.Put(ctx, id, session); err != nil {
		_ = s.operations.Fail(ctx, user, id, "pending", "cache_unavailable")
		return nil, ErrSupplierAuthorizationUnavailable
	}
	return &SupplierAuthorizationStart{AuthURL: openai.BuildAuthorizationURLForPlatform(state, openai.GenerateCodeChallenge(verifier), s.config.RedirectURI, openai.OAuthPlatformOpenAI), SessionID: id}, nil
}
func (s *SupplierAuthorizationService) Complete(ctx context.Context, user int64, id, state, code, purpose string, target int64) (*SupplierAuthorizationResult, error) {
	if s.operations == nil || s.authorizer == nil || user <= 0 || id == "" || state == "" || code == "" || !validSupplierPurpose(purpose, target) {
		return nil, ErrSupplierAuthorizationInvalid
	}
	if err := s.authorizer.AuthorizeSupplierAuthorization(ctx, user, purpose, target); err != nil {
		return nil, err
	}
	op, err := s.Status(ctx, user, id)
	if err != nil {
		return nil, err
	}
	digest := authorizationDigest(state, code, purpose, target)
	if op.Purpose != purpose || op.Target != target || op.Provider != s.config.Provider || subtle.ConstantTimeCompare([]byte(op.StateDigest), []byte(authorizationDigest(state))) != 1 || (op.CallbackDigest != "" && subtle.ConstantTimeCompare([]byte(op.CallbackDigest), []byte(digest)) != 1) {
		return nil, ErrSupplierAuthorizationInvalid
	}
	switch op.Status {
	case "completed":
		if op.CallbackDigest == "" || op.AccountID == nil || *op.AccountID <= 0 || (purpose == "reauthorize" && *op.AccountID != target) {
			return nil, ErrSupplierAuthorizationNeedsReview
		}
		return &SupplierAuthorizationResult{AccountID: *op.AccountID}, nil
	case "processing":
		return nil, ErrSupplierAuthorizationInProgress
	case "needs_review":
		return nil, ErrSupplierAuthorizationNeedsReview
	case "failed":
		return nil, ErrSupplierAuthorizationFailed
	case "pending":
	default:
		return nil, ErrSupplierAuthorizationFailed
	}
	claimed, err := s.operations.Claim(ctx, user, id, digest)
	if err != nil {
		return nil, ErrSupplierAuthorizationUnavailable
	}
	if !claimed {
		return s.Complete(ctx, user, id, state, code, purpose, target)
	}
	v, err := s.store.Consume(ctx, id, user)
	if err != nil {
		_ = s.operations.Fail(ctx, user, id, "processing", "session_expired")
		return nil, ErrSupplierAuthorizationInvalid
	}
	clientID, _ := openai.OAuthClientConfigByPlatform(openai.OAuthPlatformOpenAI)
	if v.UserID != user || v.Purpose != purpose || v.AccountID != target || v.Provider != s.config.Provider || v.RedirectURI != s.config.RedirectURI || v.ProxyURL != s.config.ProxyURL || v.ClientID != clientID || v.CodeVerifier == "" || subtle.ConstantTimeCompare([]byte(v.State), []byte(state)) != 1 {
		_ = s.operations.Fail(ctx, user, id, "processing", "invalid_session")
		return nil, ErrSupplierAuthorizationInvalid
	}
	token, err := s.exchange.ExchangeCode(ctx, code, v.CodeVerifier, v.RedirectURI, v.ProxyURL, v.ClientID)
	if err != nil || token == nil || token.AccessToken == "" {
		_ = s.operations.Fail(ctx, user, id, "processing", "exchange_failed")
		return nil, ErrSupplierAuthorizationFailed
	}
	account, err := s.writer.SaveSupplierAuthorization(WithSupplierAuthorizationOperation(ctx, id), user, purpose, target, v.Provider, token)
	// A failed call may have committed. Observe the durable result before classifying it.
	persisted, lookupErr := s.operations.Get(ctx, user, id)
	if lookupErr == nil && persisted != nil && persisted.Status == "completed" && persisted.AccountID != nil && *persisted.AccountID > 0 && (purpose != "reauthorize" || *persisted.AccountID == target) {
		return &SupplierAuthorizationResult{AccountID: *persisted.AccountID}, nil
	}
	if err != nil || account <= 0 || lookupErr != nil || persisted == nil || persisted.Status != "completed" {
		_ = s.operations.Fail(ctx, user, id, "processing", "write_uncertain")
		return nil, ErrSupplierAuthorizationNeedsReview
	}
	return nil, ErrSupplierAuthorizationNeedsReview
}
