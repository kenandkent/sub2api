//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type replayResolverTest struct {
	found bool
	calls int
	fail  bool
}

func (r *replayResolverTest) MarketplaceReplayExists(_ context.Context, buyer, key, group int64, id string) (bool, error) {
	r.calls++
	if r.fail {
		return false, errors.New("resolver unavailable")
	}
	return r.found && buyer == 10 && key == 20 && group == 30 && id == "matching", nil
}
func TestMarketplaceReplayAdmissionIsScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := &service.User{ID: 10, Status: service.StatusActive, Balance: 0}
	group := &service.Group{ID: 30, Status: service.StatusActive, Platform: service.PlatformOpenAI}
	key := &service.APIKey{ID: 20, UserID: 10, Key: "key", Status: service.StatusAPIKeyQuotaExhausted, User: user, Group: group, GroupID: &group.ID}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { k := *key; return &k, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	resolver := &replayResolverTest{found: true}
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg, resolver)))
	router.Any("/*path", func(c *gin.Context) { c.JSON(200, gin.H{"replay": IsMarketplaceReplayOnly(c)}) })
	request := func(method, path, id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("x-api-key", "key")
		req.Header.Set("Idempotency-Key", id)
		router.ServeHTTP(w, req)
		return w
	}
	w := request("POST", "/v1/responses", "matching")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"replay":true`)
	require.NotEqual(t, 200, request("POST", "/v1/responses", "missing").Code)
	require.NotEqual(t, 200, request("POST", "/v1/messages", "matching").Code)
	require.NotEqual(t, 200, request("GET", "/v1/responses", "matching").Code)
	expired := time.Now().Add(-time.Second)
	key.ExpiresAt = &expired
	require.NotEqual(t, 200, request("POST", "/v1/responses", "matching").Code)
	key.ExpiresAt = nil
	key.Status = service.StatusDisabled
	require.Equal(t, http.StatusUnauthorized, request("POST", "/v1/responses", "matching").Code)
	// A resolver outage must not alter ordinary admission or grant a replay exception.
	key.Status = service.StatusActive
	user.Balance = 10
	resolver.fail = true
	w = request("POST", "/v1/responses", "matching")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"replay":false`)
}
