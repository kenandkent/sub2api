//go:build integration

package repository

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type reviewSecurityUserRepo struct{ service.UserRepository }

func (reviewSecurityUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, TotpEnabled: true}, nil
}
func (reviewSecurityUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type reviewSecurityGrant struct{ service.TotpCache }

func (reviewSecurityGrant) HasStepUpGrant(context.Context, int64, string) (bool, error) {
	return true, nil
}

func TestHTTPNonSupplierCannotCreateDestination(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var user int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES ('review-http@example.test','x') RETURNING id`).Scan(&user))
	var profiles int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM supplier_profiles WHERE user_id=$1`, user).Scan(&profiles))
	require.Zero(t, profiles)
	env, err := service.NewCredentialEnvelope("test", map[string][]byte{"test": make([]byte, 32)})
	require.NoError(t, err)
	users := reviewSecurityUserRepo{}
	h := handler.NewSupplierPayoutHandler(service.NewSupplierPayoutService(NewSupplierPayoutRepository(integrationDB), nil, env), service.NewTotpService(users, nil, reviewSecurityGrant{}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user})
		c.Set("auth_method", service.AuditAuthMethodJWT)
		c.Next()
	})
	router.POST("/api/v1/supplier/payout-destinations", h.CreateDestination)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/supplier/payout-destinations", bytes.NewBufferString(`{"masked_label":"bank ending 1234","credential":"test-bank-data"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "review-http")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM supplier_payout_destinations WHERE supplier_id=$1`, user).Scan(&count))
	require.Zero(t, count)
}

func TestHTTPSupplierReviewRejectsStaleVersionAndAuditsSnapshot(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var user, admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES ('review-profile@example.test','x') RETURNING id`).Scan(&user))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES ('review-profile-admin@example.test','x','admin') RETURNING id`).Scan(&admin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status,version,agreement_version,application_fields) VALUES ($1,'pending',2,7,'{"contact":"supplier"}')`, user)
	require.NoError(t, err)
	h := handler.NewSupplierHandler(service.NewSupplierService(NewSupplierRepository(integrationDB), nil))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: admin})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
		c.Set("auth_method", service.AuditAuthMethodJWT)
		c.Next()
	})
	router.POST("/suppliers/:id/review", h.AdminReview)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/suppliers/%d/review", user), bytes.NewBufferString(`{"status":"active","reason":"approved stale data","expected_version":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM supplier_profiles WHERE user_id=$1`, user).Scan(&status))
	require.Equal(t, "pending", status)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM supplier_review_audits WHERE supplier_user_id=$1`, user).Scan(&count))
	require.Zero(t, count)
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/suppliers/%d/review", user), bytes.NewBufferString(`{"status":"active","reason":"approved current data","expected_version":2}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var reviewed, agreement int64
	var contact string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT reviewed_version,agreement_version,application_fields->>'contact' FROM supplier_review_audits WHERE supplier_user_id=$1`, user).Scan(&reviewed, &agreement, &contact))
	require.Equal(t, int64(2), reviewed)
	require.Equal(t, int64(7), agreement)
	require.Equal(t, "supplier", contact)
}
