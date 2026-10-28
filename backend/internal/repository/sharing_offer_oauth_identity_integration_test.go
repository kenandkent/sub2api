//go:build integration

package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSharingOfferOAuthIdentityAndReauthorization(t *testing.T) {
	isolateCommittedIntegrationTest(t)
	ctx := context.Background()
	env, err := service.NewCredentialEnvelope("test", map[string][]byte{"test": bytes.Repeat([]byte{9}, 32)})
	require.NoError(t, err)
	repo := NewSharingOfferRepository(integrationDB, env)
	var proxy, nextProxy int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO proxies(name,protocol,host,port) VALUES($1,'http','proxy.example',8080) RETURNING id`, uniqueTestValue(t, "oauth-proxy")).Scan(&proxy))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO proxies(name,protocol,host,port) VALUES($1,'http','proxy.example',8080) RETURNING id`, uniqueTestValue(t, "oauth-proxy")).Scan(&nextProxy))
	defaults, ok := any(repo).(interface{ SetProviderProxy(*int64) })
	require.True(t, ok, "OAuth writer must accept the trusted provider proxy")
	defaults.SetProviderProxy(&proxy)
	accounts := NewSupplierAccountRepository(integrationDB, env)
	var owner, other, group int64
	for i, id := range []*int64{&owner, &other} {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'hash') RETURNING id`, fmt.Sprintf("%s-%d@example.com", uniqueTestValue(t, "oauth-owner"), i)).Scan(id))
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, *id)
		require.NoError(t, err)
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name) VALUES($1) RETURNING id`, uniqueTestValue(t, "oauth-group")).Scan(&group))
	operation := func(user int64, key, purpose string, target int64) context.Context {
		_, e := integrationDB.ExecContext(ctx, `INSERT INTO supplier_operations(user_id,operation_key,kind,request_digest,status,metadata) VALUES($1,$2,'authorization','digest','processing',jsonb_build_object('provider','openai','purpose',$3::text,'account_id',$4::bigint))`, user, key, purpose, target)
		require.NoError(t, e)
		return service.WithSupplierAuthorizationOperation(ctx, key)
	}
	jwt := func(iss string, aud []string, exp int64, identity string) string {
		claims, e := json.Marshal(map[string]any{"iss": iss, "aud": aud, "exp": exp, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": identity, "chatgpt_user_id": "user-private"}})
		require.NoError(t, e)
		return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	}
	token := func(idToken, secret string) *openai.TokenResponse {
		return &openai.TokenResponse{AccessToken: secret, RefreshToken: "refresh-private", IDToken: idToken, ExpiresIn: 3600}
	}
	valid := jwt("https://auth.openai.com", []string{openai.ClientID}, time.Now().Add(time.Hour).Unix(), "account-private")
	id, err := repo.SaveSupplierAuthorization(operation(owner, "create", "create", 0), owner, "create", 0, "openai", token(valid, "access-private"))
	require.NoError(t, err)
	var actualProxy int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT proxy_id FROM accounts WHERE id=$1`, id).Scan(&actualProxy))
	require.Equal(t, proxy, actualProxy)
	var raw string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials::text FROM accounts WHERE id=$1`, id).Scan(&raw))
	require.NotContains(t, raw, "account-private")
	require.NotContains(t, raw, "access-private")
	var payload struct {
		Envelope service.EncryptedCredential `json:"_supplier_envelope"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	plain, err := env.Open("openai", owner, id, payload.Envelope)
	require.NoError(t, err)
	var creds map[string]any
	require.NoError(t, json.Unmarshal(plain, &creds))
	require.Equal(t, "account-private", creds["chatgpt_account_id"])
	require.Equal(t, "user-private", creds["chatgpt_user_id"])
	var native bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (extra->>'openai_responses_supported')::boolean FROM accounts WHERE id=$1`, id).Scan(&native))
	require.True(t, native)
	fingerprint := sha256.Sum256([]byte("account-private"))
	var stored string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT fingerprint FROM marketplace_credential_fingerprints WHERE account_id=$1 AND provider='openai:oauth'`, id).Scan(&stored))
	require.Equal(t, hex.EncodeToString(fingerprint[:]), stored)
	_, err = repo.SaveSupplierAuthorization(operation(other, "duplicate", "create", 0), other, "create", 0, "openai", token(valid, "different-secret"))
	require.Error(t, err)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE owner_user_id=$1`, other).Scan(&count))
	require.Zero(t, count)
	_, err = integrationDB.ExecContext(ctx, `UPDATE sharing_offers SET allowed_models='["gpt-4o"]',review_status='pending_review' WHERE account_id=$1`, id)
	require.NoError(t, err)
	a, err := accounts.Get(ctx, owner, id)
	require.NoError(t, err)
	dto, err := json.Marshal(a)
	require.NoError(t, err)
	require.NotContains(t, string(dto), "account-private")
	require.NotContains(t, string(dto), "access-private")
	_, err = accounts.Review(ctx, other, a.Offer.ID, a.Offer.Version, "approved", "reviewed", []int64{group})
	require.NoError(t, err)
	// Owner and operation bindings are checked before any credential or review changes.
	_, err = repo.SaveSupplierAuthorization(operation(other, "wrong-owner", "reauthorize", id), other, "reauthorize", id, "openai", token(valid, "wrong-secret"))
	require.Error(t, err)
	defaults.SetProviderProxy(&nextProxy)
	reauth := operation(owner, "reauth", "reauthorize", id)
	_, err = repo.SaveSupplierAuthorization(reauth, owner, "reauthorize", id, "openai", token(valid, "new-access"))
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT proxy_id FROM accounts WHERE id=$1`, id).Scan(&actualProxy))
	require.Equal(t, nextProxy, actualProxy)
	_, err = repo.SaveSupplierAuthorization(reauth, owner, "reauthorize", id, "openai", token(valid, "replay"))
	require.Error(t, err)
	a, err = accounts.Get(ctx, owner, id)
	require.NoError(t, err)
	require.Equal(t, "pending_review", a.Offer.ReviewStatus)
	require.False(t, a.Offer.Enabled)
	// An identity collision during reauthorization must roll back credentials,
	// old fingerprint, operation completion, and offer state together.
	otherToken := jwt("https://auth.openai.com", []string{openai.ClientID}, time.Now().Add(time.Hour).Unix(), "other-account")
	_, err = repo.SaveSupplierAuthorization(operation(other, "other-create", "create", 0), other, "create", 0, "openai", token(otherToken, "other-access"))
	require.NoError(t, err)
	var before, after, operationStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials::text FROM accounts WHERE id=$1`, id).Scan(&before))
	defaults.SetProviderProxy(&proxy)
	_, err = repo.SaveSupplierAuthorization(operation(owner, "conflicting-reauth", "reauthorize", id), owner, "reauthorize", id, "openai", token(otherToken, "collision-secret"))
	require.Error(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials::text FROM accounts WHERE id=$1`, id).Scan(&after))
	require.Equal(t, before, after)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT proxy_id FROM accounts WHERE id=$1`, id).Scan(&actualProxy))
	require.Equal(t, nextProxy, actualProxy)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT fingerprint FROM marketplace_credential_fingerprints WHERE account_id=$1`, id).Scan(&stored))
	require.Equal(t, hex.EncodeToString(fingerprint[:]), stored)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM supplier_operations WHERE user_id=$1 AND operation_key='conflicting-reauth'`, owner).Scan(&operationStatus))
	require.Equal(t, "processing", operationStatus)
	// Reauthorization with unverifiable claims revokes the old fingerprint and
	// disables the offer until another verified exchange and manual review.
	_, err = repo.SaveSupplierAuthorization(operation(owner, "unverified-reauth", "reauthorize", id), owner, "reauthorize", id, "openai", token("", "unknown-access"))
	require.NoError(t, err)
	a, err = accounts.Get(ctx, owner, id)
	require.NoError(t, err)
	require.Equal(t, "draft", a.Offer.ReviewStatus)
	require.Equal(t, "pending", a.CredentialStatus)
	require.False(t, a.Offer.Enabled)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_credential_fingerprints WHERE account_id=$1`, id).Scan(&count))
	require.Zero(t, count)
	// Each malformed or incomplete claim set stays unverified and cannot be approved.
	bad := []string{"", jwt("https://invalid.example", []string{openai.ClientID}, time.Now().Add(time.Hour).Unix(), "one"), jwt("https://auth.openai.com", []string{"wrong"}, time.Now().Add(time.Hour).Unix(), "two"), jwt("https://auth.openai.com", []string{openai.ClientID}, time.Now().Add(-time.Hour).Unix(), "expired"), jwt("https://auth.openai.com", []string{openai.ClientID}, 0, "three"), jwt("https://auth.openai.com", []string{openai.ClientID}, time.Now().Add(time.Hour).Unix(), "")}
	for i, v := range bad {
		badID, e := repo.SaveSupplierAuthorization(operation(owner, fmt.Sprintf("bad-%d", i), "create", 0), owner, "create", 0, "openai", token(v, "token"))
		require.NoError(t, e)
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM marketplace_credential_fingerprints WHERE account_id=$1`, badID).Scan(&count))
		require.Zero(t, count)
		unverified, e := accounts.Get(ctx, owner, badID)
		require.NoError(t, e)
		require.Equal(t, "pending", unverified.CredentialStatus)
		_, e = integrationDB.ExecContext(ctx, `UPDATE sharing_offers SET allowed_models='["gpt-4o"]',review_status='pending_review' WHERE account_id=$1`, badID)
		require.NoError(t, e)
		offer, e := accounts.Get(ctx, owner, badID)
		require.NoError(t, e)
		_, e = accounts.Review(ctx, other, offer.Offer.ID, offer.Offer.Version, "approved", "reviewed", []int64{group})
		require.Error(t, e)
	}
}
