//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type vaultRotationPort interface {
	RotateCredentialVault(context.Context, int64, string, string, int, *service.CredentialEnvelope) (int, int, bool, error)
}

type vaultTestUsers struct{ service.UserRepository }

func (vaultTestUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, TotpEnabled: true}, nil
}
func (vaultTestUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type vaultTestGrant struct{ service.TotpCache }

func (vaultTestGrant) HasStepUpGrant(context.Context, int64, string) (bool, error) { return true, nil }

func TestVaultRotationHTTPWithRealRepository(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, uniqueTestValue(t, "vault-http")+"@example.com").Scan(&admin))
	env, err := service.NewCredentialEnvelope("current", map[string][]byte{"current": bytes.Repeat([]byte{7}, 32)})
	require.NoError(t, err)
	users := vaultTestUsers{}
	h := handler.NewMarketplaceVaultRotationHandler(NewMarketplaceVaultRotationRepository(integrationDB, env), service.NewTotpService(users, nil, vaultTestGrant{}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: admin})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
		c.Set("auth_method", service.AuditAuthMethodJWT)
		c.Next()
	})
	router.POST("/admin/marketplace/vault/rotate", h.Rotate)
	req := httptest.NewRequest(http.MethodPost, "/admin/marketplace/vault/rotate", bytes.NewBufferString(`{"operation_id":"http-vault","limit":3,"reason":"routine rotation"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"completed":true`)
	require.NotContains(t, w.Body.String(), "reason")
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_vault_rotation_operations WHERE operation_id='http-vault' AND actor_user_id=$1 AND status='completed'`, admin).Scan(&count))
	require.Equal(t, 1, count)
}

func TestVaultRotationBatchesRetryAndOldKeyRetirement(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var owner, admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, uniqueTestValue(t, "vault-owner")+"@example.com").Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, uniqueTestValue(t, "vault-admin")+"@example.com").Scan(&admin))
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, owner)
	require.NoError(t, err)
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	old, err := service.NewCredentialEnvelope("old", map[string][]byte{"old": oldKey})
	require.NoError(t, err)
	current, err := service.NewCredentialEnvelope("new", map[string][]byte{"old": oldKey, "new": newKey})
	require.NoError(t, err)
	var accounts []int64
	for _, typ := range []string{"apikey", "oauth"} {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(owner_user_id,name,platform,type,credentials) VALUES($1,$2,'openai',$3,'{}') RETURNING id`, owner, typ, typ).Scan(&id))
		sealed, e := old.Seal("openai", owner, id, []byte(`{"token":"private"}`))
		require.NoError(t, e)
		payload, e := json.Marshal(sealed)
		require.NoError(t, e)
		_, e = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_build_object('_supplier_envelope',$1::jsonb,'base_url','https://example.test') WHERE id=$2`, string(payload), id)
		require.NoError(t, e)
		accounts = append(accounts, id)
	}
	var destination int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('supplier_payout_destinations','id'))`).Scan(&destination))
	sealed, err := old.Seal("payout", owner, destination, []byte("private-bank"))
	require.NoError(t, err)
	payload, err := json.Marshal(sealed)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_payout_destinations(id,supplier_id,masked_label,credential,version) VALUES($1,$2,'ending 1234',$3::jsonb,1)`, destination, owner, string(payload))
	require.NoError(t, err)
	_, err = NewSupplierPayoutRepository(integrationDB).VerifyDestination(ctx, destination, admin, "vault-verify", "verified")
	require.NoError(t, err)
	var unverified int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('supplier_payout_destinations','id'))`).Scan(&unverified))
	sealed, err = old.Seal("payout", owner, unverified, []byte("private-bank"))
	require.NoError(t, err)
	payload, err = json.Marshal(sealed)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_payout_destinations(id,supplier_id,masked_label,credential,version) VALUES($1,$2,'ending 5678',$3::jsonb,2)`, unverified, owner, string(payload))
	require.NoError(t, err)
	var payout int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO supplier_payouts(supplier_id,destination_id,masked_label,amount,status) VALUES($1,$2,'ending 1234',1,'requested') RETURNING id`, owner, destination).Scan(&payout))
	rotator, ok := any(NewSupplierPayoutRepository(integrationDB)).(vaultRotationPort)
	require.True(t, ok, "vault rotation must be available")
	processed, remaining, complete, err := rotator.RotateCredentialVault(ctx, admin, "vault-batch-1", "routine rotation", 2, current)
	require.NoError(t, err)
	require.Equal(t, 2, processed)
	require.Equal(t, 2, remaining)
	require.False(t, complete)
	p, r, c, err := rotator.RotateCredentialVault(ctx, admin, "vault-batch-1", "routine rotation", 2, current)
	require.NoError(t, err)
	require.Equal(t, processed, p)
	require.Equal(t, remaining, r)
	require.Equal(t, complete, c)
	_, _, _, err = rotator.RotateCredentialVault(ctx, admin, "vault-batch-1", "different reason", 2, current)
	require.Error(t, err)
	p, r, c, err = rotator.RotateCredentialVault(ctx, admin, "vault-batch-2", "routine rotation", 2, current)
	require.NoError(t, err)
	require.Equal(t, 2, p)
	require.Zero(t, r)
	require.True(t, c)
	newOnly, err := service.NewCredentialEnvelope("new", map[string][]byte{"new": newKey})
	require.NoError(t, err)
	for _, id := range accounts {
		var raw []byte
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials->'_supplier_envelope' FROM accounts WHERE id=$1`, id).Scan(&raw))
		var enc service.EncryptedCredential
		require.NoError(t, json.Unmarshal(raw, &enc))
		plain, e := newOnly.Open("openai", owner, id, enc)
		require.NoError(t, e)
		require.Equal(t, `{"token":"private"}`, string(plain))
	}
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credential FROM supplier_payout_destinations WHERE id=$1`, destination).Scan(&raw))
	var enc service.EncryptedCredential
	require.NoError(t, json.Unmarshal(raw, &enc))
	plain, err := newOnly.Open("payout", owner, destination, enc)
	require.NoError(t, err)
	require.Equal(t, "private-bank", string(plain))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credential FROM supplier_payout_destinations WHERE id=$1`, unverified).Scan(&raw))
	require.NoError(t, json.Unmarshal(raw, &enc))
	plain, err = newOnly.Open("payout", owner, unverified, enc)
	require.NoError(t, err)
	require.Equal(t, "private-bank", string(plain))
	var version int
	var verified bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT version,verified FROM supplier_payout_destinations WHERE id=$1`, destination).Scan(&version, &verified))
	require.Equal(t, 1, version)
	require.True(t, verified)
	var payoutDestination int64
	var payoutMask, payoutStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT destination_id,masked_label,status FROM supplier_payouts WHERE id=$1`, payout).Scan(&payoutDestination, &payoutMask, &payoutStatus))
	require.Equal(t, destination, payoutDestination)
	require.Equal(t, "ending 1234", payoutMask)
	require.Equal(t, "requested", payoutStatus)
	_, err = integrationDB.ExecContext(ctx, `UPDATE supplier_payout_destinations SET credential='{}' WHERE id=$1`, destination)
	require.Error(t, err, "ordinary writes cannot bypass destination immutability")
	var auditText string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT row_to_json(v)::text FROM marketplace_vault_rotation_operations v WHERE operation_id='vault-batch-1'`).Scan(&auditText))
	require.NotContains(t, auditText, "private")
}

func TestVaultRotationRollbackAndConcurrentReplay(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	var owner, admin int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, uniqueTestValue(t, "vault-rollback-owner")+"@example.com").Scan(&owner))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'x','admin') RETURNING id`, uniqueTestValue(t, "vault-rollback-admin")+"@example.com").Scan(&admin))
	oldKey, missingKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{2}, 32)
	old, err := service.NewCredentialEnvelope("old", map[string][]byte{"old": oldKey})
	require.NoError(t, err)
	missing, err := service.NewCredentialEnvelope("missing", map[string][]byte{"missing": missingKey})
	require.NoError(t, err)
	partial, err := service.NewCredentialEnvelope("new", map[string][]byte{"old": oldKey, "new": newKey})
	require.NoError(t, err)
	full, err := service.NewCredentialEnvelope("new", map[string][]byte{"old": oldKey, "missing": missingKey, "new": newKey})
	require.NoError(t, err)
	var ids []int64
	for i, env := range []*service.CredentialEnvelope{old, missing} {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(owner_user_id,name,platform,type,credentials) VALUES($1,$2,'openai','apikey','{}') RETURNING id`, owner, fmt.Sprintf("rotation-%d", i)).Scan(&id))
		enc, e := env.Seal("openai", owner, id, []byte("credential"))
		require.NoError(t, e)
		raw, e := json.Marshal(enc)
		require.NoError(t, e)
		_, e = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_build_object('_supplier_envelope',$1::jsonb) WHERE id=$2`, string(raw), id)
		require.NoError(t, e)
		ids = append(ids, id)
	}
	r := NewSupplierPayoutRepository(integrationDB)
	_, _, _, err = r.RotateCredentialVault(ctx, owner, "unauthorized", "rotation", 2, full)
	require.Error(t, err)
	_, _, _, err = r.RotateCredentialVault(ctx, admin, "retry-after-failure", "rotation", 2, partial)
	require.Error(t, err)
	var key string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials->'_supplier_envelope'->>'key_id' FROM accounts WHERE id=$1`, ids[0]).Scan(&key))
	require.Equal(t, "old", key)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_vault_rotation_operations WHERE operation_id='retry-after-failure'`).Scan(&count))
	require.Zero(t, count)
	var wg sync.WaitGroup
	type result struct {
		processed, remaining int
		completed            bool
		err                  error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, left, done, e := r.RotateCredentialVault(ctx, admin, "retry-after-failure", "rotation", 2, full)
			results <- result{p, left, done, e}
		}()
	}
	wg.Wait()
	close(results)
	for got := range results {
		require.NoError(t, got.err)
		require.Equal(t, 2, got.processed)
		require.Zero(t, got.remaining)
		require.True(t, got.completed)
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_vault_rotation_operations WHERE operation_id='retry-after-failure' AND status='completed'`).Scan(&count))
	require.Equal(t, 1, count)
}
