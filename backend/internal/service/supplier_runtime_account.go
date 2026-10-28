package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

var ErrSupplierRuntimeUnavailable = errors.New("supplier credentials unavailable")

// SupplierRuntimeCredentialStore reads the current envelope and atomically replaces
// it only if the expected envelope remains current. Implementations must scope by owner.
type SupplierRuntimeCredentialStore interface {
	LoadSupplierCredential(context.Context, int64, int64) (EncryptedCredential, error)
	CompareAndSwapSupplierCredential(context.Context, int64, int64, EncryptedCredential, EncryptedCredential) (bool, error)
}
type SupplierRuntimeOAuthRefresher interface {
	RefreshToken(context.Context, string, string) (*openai.TokenResponse, error)
}
type SupplierRuntimeRefreshLock interface {
	AcquireRefreshLock(context.Context, string, time.Duration) (bool, error)
	ReleaseRefreshLock(context.Context, string) error
}
type SupplierRuntimeProviderConfig struct {
	ProxyURL string
	BaseURL  string
}
type SupplierRuntimeAccountService struct {
	envelope *CredentialEnvelope
	store    SupplierRuntimeCredentialStore
	refresh  SupplierRuntimeOAuthRefresher
	lock     SupplierRuntimeRefreshLock
	config   SupplierRuntimeProviderConfig
}

func NewSupplierRuntimeAccountService(envelope *CredentialEnvelope, store SupplierRuntimeCredentialStore, refresh SupplierRuntimeOAuthRefresher, lock SupplierRuntimeRefreshLock, cfg SupplierRuntimeProviderConfig) *SupplierRuntimeAccountService {
	return &SupplierRuntimeAccountService{envelope: envelope, store: store, refresh: refresh, lock: lock, config: cfg}
}

// Prepare decrypts only into a request-local copy. It never writes plaintext to
// the supplied account or a token cache; failures do not fall back to plaintext.
func (s *SupplierRuntimeAccountService) Prepare(ctx context.Context, account *Account) (*Account, error) {
	if s == nil || s.envelope == nil || s.store == nil || account == nil || account.OwnerUserID == nil || *account.OwnerUserID <= 0 || account.ID <= 0 || account.Platform != PlatformOpenAI || (account.Type != AccountTypeOAuth && account.Type != AccountTypeAPIKey) || account.ParentAccountID != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	sealed, err := s.store.LoadSupplierCredential(ctx, account.ID, *account.OwnerUserID)
	if err != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	credentials, err := s.open(account, sealed)
	if err != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	copyAccount := *account
	copyAccount.Credentials = runtimePublicCredentials(account.Credentials, s.config.BaseURL)
	for k, v := range credentials {
		copyAccount.Credentials[k] = v
	}
	switch account.Type {
	case AccountTypeAPIKey:
		if strings.TrimSpace(copyAccount.GetOpenAIApiKey()) == "" {
			return nil, ErrSupplierRuntimeUnavailable
		}
	case AccountTypeOAuth:
		if strings.TrimSpace(copyAccount.GetChatGPTAccountID()) == "" {
			return nil, ErrSupplierRuntimeUnavailable
		}
		expires := copyAccount.GetCredentialAsTime("expires_at")
		if expires == nil || time.Until(*expires) <= openAITokenRefreshSkew {
			return s.refreshAccount(ctx, account)
		}
		if strings.TrimSpace(copyAccount.GetOpenAIAccessToken()) == "" {
			return nil, ErrSupplierRuntimeUnavailable
		}
	}
	return &copyAccount, nil
}
func runtimePublicCredentials(src map[string]any, baseURL string) map[string]any {
	dst := make(map[string]any)
	for _, k := range []string{"model_mapping", "openai_capabilities"} {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
	if baseURL != "" {
		dst["base_url"] = baseURL
	}
	return dst
}
func (s *SupplierRuntimeAccountService) open(account *Account, sealed EncryptedCredential) (map[string]any, error) {
	raw, err := s.envelope.Open(account.Platform, *account.OwnerUserID, account.ID, sealed)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	out := make(map[string]any)
	for _, k := range []string{"api_key", "access_token", "refresh_token", "id_token", "token_type", "scope", "expires_at", "chatgpt_account_id", "chatgpt_user_id"} {
		if v, ok := decoded[k].(string); ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s *SupplierRuntimeAccountService) refreshAccount(ctx context.Context, account *Account) (*Account, error) {
	if s.lock == nil || s.refresh == nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	key := fmt.Sprintf("supplier-runtime:%d", account.ID)
	locked, err := s.lock.AcquireRefreshLock(ctx, key, 30*time.Second)
	if err != nil || !locked {
		return nil, ErrSupplierRuntimeUnavailable
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.lock.ReleaseRefreshLock(releaseCtx, key)
	}()
	sealed, err := s.store.LoadSupplierCredential(ctx, account.ID, *account.OwnerUserID)
	if err != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	credentials, err := s.open(account, sealed)
	if err != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	current := *account
	current.Credentials = credentials
	if strings.TrimSpace(current.GetChatGPTAccountID()) == "" {
		return nil, ErrSupplierRuntimeUnavailable
	}
	expires := current.GetCredentialAsTime("expires_at")
	if expires != nil && time.Until(*expires) > openAITokenRefreshSkew && strings.TrimSpace(current.GetOpenAIAccessToken()) != "" {
		current.Credentials = runtimePublicCredentials(account.Credentials, s.config.BaseURL)
		for k, v := range credentials {
			current.Credentials[k] = v
		}
		return &current, nil
	}
	token := strings.TrimSpace(current.GetOpenAIRefreshToken())
	if token == "" {
		return nil, ErrSupplierRuntimeUnavailable
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := s.refresh.RefreshToken(refreshCtx, token, s.config.ProxyURL)
	if err != nil || response == nil || strings.TrimSpace(response.AccessToken) == "" || response.ExpiresIn <= 0 || response.ExpiresIn > 31536000 {
		return nil, ErrSupplierRuntimeUnavailable
	}
	if response.IDToken != "" {
		claims, parseErr := openai.ParseIDToken(response.IDToken)
		if parseErr != nil || claims == nil {
			return nil, ErrSupplierRuntimeUnavailable
		}
		if claims.OpenAIAuth != nil {
			newID := strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
			if newID != "" && newID != strings.TrimSpace(current.GetChatGPTAccountID()) {
				return nil, ErrSupplierRuntimeUnavailable
			}
		}
	}
	credentials["access_token"] = response.AccessToken
	if response.RefreshToken != "" {
		credentials["refresh_token"] = response.RefreshToken
	}
	if response.IDToken != "" {
		credentials["id_token"] = response.IDToken
	}
	if response.TokenType != "" {
		credentials["token_type"] = response.TokenType
	}
	if response.Scope != "" {
		credentials["scope"] = response.Scope
	}
	credentials["expires_at"] = time.Now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second).Format(time.RFC3339)
	raw, err := json.Marshal(credentials)
	if err != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	replacement, err := s.envelope.Seal(account.Platform, *account.OwnerUserID, account.ID, raw)
	for i := range raw {
		raw[i] = 0
	}
	if err != nil {
		return nil, ErrSupplierRuntimeUnavailable
	}
	swapped, err := s.store.CompareAndSwapSupplierCredential(ctx, account.ID, *account.OwnerUserID, sealed, replacement)
	if err != nil || !swapped {
		return nil, ErrSupplierRuntimeUnavailable
	}
	current.Credentials = runtimePublicCredentials(account.Credentials, s.config.BaseURL)
	for k, v := range credentials {
		current.Credentials[k] = v
	}
	return &current, nil
}
