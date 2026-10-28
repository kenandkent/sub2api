package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/url"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

var ErrSupplierAuthorizationInvalid = errors.New("supplier authorization invalid or expired")
var ErrSupplierAuthorizationFailed = errors.New("supplier authorization failed")

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
	store    SupplierAuthorizationStore
	exchange SupplierOpenAIExchanger
	writer   SupplierAuthorizationWriter
	config   SupplierAuthorizationConfig
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
func validSupplierPurpose(purpose string, target int64) bool {
	return (purpose == "create" && target == 0) || (purpose == "reauthorize" && target > 0)
}
func (s *SupplierAuthorizationService) Start(ctx context.Context, user int64, purpose string, target int64) (*SupplierAuthorizationStart, error) {
	if user <= 0 || !validSupplierPurpose(purpose, target) {
		return nil, ErrSupplierAuthorizationInvalid
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
	if err = s.store.Put(ctx, id, session); err != nil {
		return nil, ErrSupplierAuthorizationFailed
	}
	return &SupplierAuthorizationStart{AuthURL: openai.BuildAuthorizationURLForPlatform(state, openai.GenerateCodeChallenge(verifier), s.config.RedirectURI, openai.OAuthPlatformOpenAI), SessionID: id}, nil
}
func (s *SupplierAuthorizationService) Complete(ctx context.Context, user int64, id, state, code, purpose string, target int64) (*SupplierAuthorizationResult, error) {
	if user <= 0 || id == "" || state == "" || code == "" || !validSupplierPurpose(purpose, target) {
		return nil, ErrSupplierAuthorizationInvalid
	}
	v, err := s.store.Consume(ctx, id, user)
	if err != nil {
		return nil, ErrSupplierAuthorizationInvalid
	}
	clientID, _ := openai.OAuthClientConfigByPlatform(openai.OAuthPlatformOpenAI)
	if v.UserID != user || v.Purpose != purpose || v.AccountID != target || v.Provider != s.config.Provider || v.RedirectURI != s.config.RedirectURI || v.ProxyURL != s.config.ProxyURL || v.ClientID != clientID || v.CodeVerifier == "" || subtle.ConstantTimeCompare([]byte(v.State), []byte(state)) != 1 {
		return nil, ErrSupplierAuthorizationInvalid
	}
	token, err := s.exchange.ExchangeCode(ctx, code, v.CodeVerifier, v.RedirectURI, v.ProxyURL, v.ClientID)
	if err != nil || token == nil || token.AccessToken == "" {
		return nil, ErrSupplierAuthorizationFailed
	}
	account, err := s.writer.SaveSupplierAuthorization(ctx, user, purpose, target, v.Provider, token)
	if err != nil || account <= 0 || (purpose == "reauthorize" && account != target) {
		return nil, ErrSupplierAuthorizationFailed
	}
	return &SupplierAuthorizationResult{AccountID: account}, nil
}
