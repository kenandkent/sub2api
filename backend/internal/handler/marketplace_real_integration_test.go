//go:build unit && integration

package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

type realGatewayFixture struct {
	db                                *sql.DB
	redis                             *redis.Client
	buyer, owner, group, key, account int64
	cfg                               *config.Config
	env                               *service.CredentialEnvelope
	quote                             service.QuoteSnapshot
	repo                              *grokCredentialHandlerRepo
	store                             service.MarketplaceGatewayStore
}

type realGatewayCatalog struct{ quote service.QuoteSnapshot }

func (c realGatewayCatalog) IsSharedGroup(context.Context, int64) (bool, error) { return true, nil }
func (c realGatewayCatalog) QuoteTemplate(_ context.Context, _ int64, model, protocol string) (service.QuoteSnapshot, int64, int64, error) {
	q := c.quote
	q.PublicModel = model
	q.Protocol = protocol
	return q, 1000, 200, nil
}

// pauseAfterQualification simulates a supplier suspension in the narrow window
// between scheduler qualification and the transactional dispatch recheck.
type pauseAfterQualification struct {
	service.MarketplaceGatewayStore
	db    *sql.DB
	owner int64
}

func (s pauseAfterQualification) Qualify(ctx context.Context, account, buyer, group int64, model string) (int64, error) {
	offer, err := s.MarketplaceGatewayStore.Qualify(ctx, account, buyer, group, model)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE supplier_profiles SET status='suspended' WHERE user_id=$1`, s.owner)
	}
	return offer, err
}

type realGatewayUpstream struct {
	service.HTTPUpstream
	mu                sync.Mutex
	calls             int
	status            int
	contentType, body string
	err               error
	entered           chan struct{}
	release           chan struct{}
	before            func()
}

func (u *realGatewayUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	_, _ = io.Copy(io.Discard, req.Body)
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	if u.before != nil {
		u.before()
	}
	if u.entered != nil {
		select {
		case u.entered <- struct{}{}:
		default:
		}
		<-u.release
	}
	if u.err != nil {
		return nil, u.err
	}
	ct := u.contentType
	if ct == "" {
		ct = "application/json"
	}
	return &http.Response{StatusCode: u.status, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}
func (u *realGatewayUpstream) count() int { u.mu.Lock(); defer u.mu.Unlock(); return u.calls }

func newRealGatewayFixture(t *testing.T, db *sql.DB, rdb *redis.Client) *realGatewayFixture {
	t.Helper()
	ctx := context.Background()
	f := &realGatewayFixture{db: db, redis: rdb}
	suffix := uuid.NewString()
	row := func(query string, args ...any) int64 {
		var id int64
		require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&id))
		return id
	}
	exec := func(query string, args ...any) {
		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	f.buyer = row(`INSERT INTO users(email,password_hash,balance) VALUES($1,'x',5) RETURNING id`, suffix+"-buyer@example.test")
	f.owner = row(`INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, suffix+"-owner@example.test")
	f.group = row(`INSERT INTO groups(name) VALUES($1) RETURNING id`, suffix)
	f.key = row(`INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,$3,'test') RETURNING id`, f.buyer, f.group, suffix)
	exec(`INSERT INTO wallet_funding_states(user_id,migration_version,reconciled_at) VALUES($1,1,now())`, f.buyer)
	exec(`INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','test',$2,5,5)`, f.buyer, suffix)
	exec(`INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, f.owner)
	f.account = row(`INSERT INTO accounts(name,platform,type,owner_user_id) VALUES($1,'openai','apikey',$2) RETURNING id`, suffix, f.owner)
	exec(`INSERT INTO sharing_offers(account_id,owner_user_id,review_status,enabled,allowed_models) VALUES($1,$2,'approved',true,'["gpt-5.4"]')`, f.account, f.owner)
	exec(`INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, f.account, f.group)
	var err error
	f.env, err = service.NewCredentialEnvelope("test", map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)})
	require.NoError(t, err)
	sealed, err := f.env.Seal("openai", f.owner, f.account, []byte(`{"api_key":"fake-upstream-key"}`))
	require.NoError(t, err)
	encoded, err := json.Marshal(sealed)
	require.NoError(t, err)
	exec(`UPDATE accounts SET credentials=jsonb_build_object('_supplier_envelope',$1::jsonb) WHERE id=$2`, string(encoded), f.account)
	rate := decimal.NewFromInt(1)
	input := service.Money{Decimal: decimal.NewFromInt(2)}
	output := service.Money{Decimal: decimal.NewFromInt(4)}
	cache := service.Money{Decimal: decimal.NewFromInt(1)}
	tiers := []service.MarketplacePriceTier{{InputPerMillion: &input, OutputPerMillion: &output, CacheReadPerMillion: &cache, CacheWritePerMillion: &input}}
	var version int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM marketplace_price_versions`).Scan(&version))
	groups := []service.MarketplaceCatalogGroup{}
	for _, protocol := range []string{"responses", "chat_completions"} {
		groups = append(groups, service.MarketplaceCatalogGroup{GroupID: f.group, Protocol: protocol, Models: []service.MarketplaceCatalogModel{{PublicModel: "public", ActualModel: "gpt-5.4", RateMultiplier: &rate, Tiers: tiers}}})
	}
	data, err := json.Marshal(service.MarketplacePriceBook{Groups: groups})
	require.NoError(t, err)
	exec(`INSERT INTO marketplace_policies(version,fee_bps,hold_hours,max_supplier_concurrency,minimum_payout_usd,published_by,published_at) VALUES($1,1000,0,1,0,$2,now())`, version, f.owner)
	exec(`INSERT INTO marketplace_price_versions(version,rules,published_by,published_at) VALUES($1,$2,$3,now())`, version, string(data), f.owner)
	f.quote = service.QuoteSnapshot{Currency: "USD", PublicModel: "public", ActualModel: "gpt-5.4", PriceVersion: version, PolicyVersion: version, RateMultiplier: &rate, Tiers: tiers, FeeBPS: 1000, SnapshotAt: time.Now().UTC()}
	f.cfg = &config.Config{RunMode: config.RunModeStandard, Marketplace: config.MarketplaceConfig{Enabled: true, DispatchEnabled: true, Providers: []config.MarketplaceProviderConfig{{Platform: "openai", AccountTypes: []string{"apikey"}, Models: []string{"gpt-5.4"}}}}}
	account := service.Account{ID: f.account, OwnerUserID: &f.owner, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{f.group}, Credentials: map[string]any{"_supplier_envelope": sealed}, Extra: map[string]any{"openai_responses_supported": true}}
	f.repo = &grokCredentialHandlerRepo{accounts: []service.Account{account}, missingOnGet: map[int64]bool{}}
	return f
}
func (f *realGatewayFixture) router(t *testing.T, up *realGatewayUpstream) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cache := newHandlerInflightCache(100)
	billing := service.NewBillingCacheService(cache, nil, nil, nil, nil, nil, f.cfg, nil)
	t.Cleanup(billing.Stop)
	concurrency := service.NewConcurrencyService(repository.NewConcurrencyCache(f.redis, 15, 900))
	gateway := service.NewOpenAIGatewayService(f.repo, nil, nil, nil, nil, repository.NewUserGroupRateRepository(f.db), nil, f.cfg, nil, concurrency, service.NewBillingService(f.cfg, nil), nil, billing, up, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, f.cfg)
	runtime := service.NewSupplierRuntimeAccountService(f.env, repository.NewSupplierRuntimeCredentialRepository(f.db), nil, nil, service.SupplierRuntimeProviderConfig{})
	store := f.store
	if store == nil {
		store = repository.NewMarketplaceGatewayRepository(f.db, f.cfg)
	}
	h.SetMarketplaceGateway(service.NewMarketplaceGateway(realGatewayCatalog{f.quote}, store, runtime))
	var group service.Group
	require.NoError(t, f.db.QueryRow(`SELECT rate_multiplier,subscription_type,peak_rate_enabled,peak_start,peak_end,peak_rate_multiplier FROM groups WHERE id=$1`, f.group).Scan(&group.RateMultiplier, &group.SubscriptionType, &group.PeakRateEnabled, &group.PeakStart, &group.PeakEnd, &group.PeakRateMultiplier))
	group.ID, group.Platform, group.Status = f.group, service.PlatformOpenAI, service.StatusActive
	key := &service.APIKey{ID: f.key, UserID: f.buyer, GroupID: &f.group, User: &service.User{ID: f.buyer, Status: service.StatusActive}, Group: &group}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.buyer, Concurrency: 10})
		c.Next()
	})
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/chat/completions", h.ChatCompletions)
	return router
}
func sendReal(router http.Handler, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestMarketplaceGatewayRealInfrastructure(t *testing.T) {
	// Avoid the Ryuk helper's Docker socket bind mount. Containers are explicitly terminated by Cleanup.
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("marketplace_test"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	rc, err := tcredis.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rc.Terminate(ctx) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	addr, err := rc.Endpoint(ctx, "")
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, rdb.Ping(ctx).Err())
	t.Run("consumer override is frozen and settled", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		_, err := db.Exec(`UPDATE groups SET rate_multiplier=2 WHERE id=$1`, f.group)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO user_group_rate_multipliers(user_id,group_id,rate_multiplier) VALUES($1,$2,0.75)`, f.buyer, f.group)
		require.NoError(t, err)
		up := &realGatewayUpstream{status: 200, body: `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}`}
		up.before = func() {
			_, updateErr := db.Exec(`UPDATE user_group_rate_multipliers SET rate_multiplier=0.5 WHERE user_id=$1 AND group_id=$2`, f.buyer, f.group)
			require.NoError(t, updateErr)
		}
		rec := sendReal(f.router(t, up), "/v1/responses", `{"model":"public","input":"hello"}`, "")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var charge decimal.Decimal
		var quote []byte
		require.NoError(t, db.QueryRow(`SELECT r.actual_charge,a.quote FROM marketplace_requests r JOIN marketplace_attempts a ON a.id=r.billable_attempt_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&charge, &quote))
		require.True(t, charge.Equal(decimal.RequireFromString("0.00002475")), charge)
		var q service.QuoteSnapshot
		require.NoError(t, json.Unmarshal(quote, &q))
		require.Equal(t, "1", q.PublishedRateMultiplier.String())
		require.Equal(t, "0.75", q.ConsumerRateMultiplier.String())
		require.Equal(t, "0.75", q.RateMultiplier.String())
	})
	t.Run("responses settlement and duplicate", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		up := &realGatewayUpstream{status: 200, body: `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}`}
		// Publish a different price after dispatch, before settlement. Capture must
		// use the attempt's immutable version and cached-token rates.
		up.before = func() {
			version := f.quote.PriceVersion + 1
			rate := decimal.NewFromInt(1)
			expensive := service.Money{Decimal: decimal.NewFromInt(99)}
			book := service.MarketplacePriceBook{Groups: []service.MarketplaceCatalogGroup{{GroupID: f.group, Protocol: "responses", Models: []service.MarketplaceCatalogModel{{PublicModel: "public", ActualModel: "gpt-5.4", RateMultiplier: &rate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &expensive, OutputPerMillion: &expensive, CacheReadPerMillion: &expensive, CacheWritePerMillion: &expensive}}}}}}}
			data, err := json.Marshal(book)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO marketplace_policies(version,fee_bps,hold_hours,max_supplier_concurrency,minimum_payout_usd,published_by,published_at) VALUES($1,0,0,1,0,$2,now())`, version, f.owner)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO marketplace_price_versions(version,rules,published_by,published_at) VALUES($1,$2,$3,now())`, version, string(data), f.owner)
			require.NoError(t, err)
		}
		router := f.router(t, up)
		body := `{"model":"public","input":"hello"}`
		rec := sendReal(router, "/v1/responses", body, "same")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Equal(t, 409, sendReal(router, "/v1/responses", body, "same").Code)
		require.Equal(t, 1, up.count())
		var state string
		var charge, balance, frozen, pending decimal.Decimal
		var captures, entries int
		var version int64
		require.NoError(t, db.QueryRow(`SELECT r.status,r.actual_charge,u.balance,u.frozen_balance,a.quote->>'price_version' FROM marketplace_requests r JOIN users u ON u.id=r.buyer_user_id JOIN marketplace_attempts a ON a.id=r.billable_attempt_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&state, &charge, &balance, &frozen, &version))
		require.Equal(t, "settled", state)
		require.True(t, charge.Equal(decimal.RequireFromString("0.000033")), charge)
		require.True(t, balance.Equal(decimal.RequireFromString("4.999967")), balance)
		require.True(t, frozen.IsZero())
		require.Equal(t, f.quote.PriceVersion, version)
		require.NoError(t, db.QueryRow(`SELECT pending FROM supplier_wallets WHERE user_id=$1`, f.owner).Scan(&pending))
		require.True(t, pending.Equal(decimal.RequireFromString("0.0000297")), pending)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_captures WHERE owner_user_id=$1`, f.owner).Scan(&captures))
		require.Equal(t, 1, captures)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM ledger_entries e JOIN marketplace_captures c ON c.ledger_transaction_id=e.transaction_id WHERE c.owner_user_id=$1`, f.owner).Scan(&entries))
		require.GreaterOrEqual(t, entries, 2)
	})
	t.Run("uncertain outcomes stay held without replay", func(t *testing.T) {
		for _, tc := range []struct {
			name, body, contentType string
			status                  int
			err                     error
		}{
			{name: "missing usage", status: 200, body: `{"id":"resp","object":"response","model":"gpt-5.4","status":"completed","output":[]}`},
			{name: "partial stream", status: 200, contentType: "text/event-stream", body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"},
			{name: "rate limit", status: 429, body: `{"error":{"message":"limited"}}`},
			{name: "network", err: fmt.Errorf("connection reset")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newRealGatewayFixture(t, db, rdb)
				up := &realGatewayUpstream{status: tc.status, body: tc.body, contentType: tc.contentType, err: tc.err}
				router := f.router(t, up)
				body := `{"model":"public","input":"hello","stream":true}`
				sendReal(router, "/v1/responses", body, "retry")
				require.Equal(t, 409, sendReal(router, "/v1/responses", body, "retry").Code)
				require.Equal(t, 1, up.count())
				var status string
				var frozen decimal.Decimal
				var count int
				require.NoError(t, db.QueryRow(`SELECT r.status,u.frozen_balance FROM marketplace_requests r JOIN users u ON u.id=r.buyer_user_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&status, &frozen))
				require.Equal(t, "needs_review", status)
				require.True(t, frozen.IsPositive())
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_captures c JOIN marketplace_requests r ON r.id=c.receipt_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&count))
				require.Zero(t, count)
			})
		}
	})
	t.Run("chat function and responses streaming settle", func(t *testing.T) {
		for _, tc := range []struct{ name, path, request, reply string }{
			{"chat function", "/v1/chat/completions", `{"model":"public","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"{}\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n"},
			{"responses stream", "/v1/responses", `{"model":"public","input":"hi","stream":true}`, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newRealGatewayFixture(t, db, rdb)
				up := &realGatewayUpstream{status: 200, contentType: "text/event-stream", body: tc.reply}
				rec := sendReal(f.router(t, up), tc.path, tc.request, "")
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.Equal(t, 1, up.count())
				if tc.name == "chat function" {
					require.Contains(t, rec.Body.String(), "lookup")
				} else {
					require.Contains(t, rec.Body.String(), "response.completed")
				}
				var status string
				var captures int
				require.NoError(t, db.QueryRow(`SELECT status FROM marketplace_requests WHERE buyer_user_id=$1`, f.buyer).Scan(&status))
				require.Equal(t, "settled", status)
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_captures WHERE owner_user_id=$1`, f.owner).Scan(&captures))
				require.Equal(t, 1, captures)
			})
		}
	})
	t.Run("suspension after qualification voids reservation", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		f.store = pauseAfterQualification{MarketplaceGatewayStore: repository.NewMarketplaceGatewayRepository(db, f.cfg), db: db, owner: f.owner}
		up := &realGatewayUpstream{status: 200, body: `{}`}
		rec := sendReal(f.router(t, up), "/v1/responses", `{"model":"public","input":"hi"}`, "")
		require.NotEqual(t, 200, rec.Code)
		require.Zero(t, up.count())
		var state string
		var frozen decimal.Decimal
		var attempts int
		require.NoError(t, db.QueryRow(`SELECT r.status,u.frozen_balance FROM marketplace_requests r JOIN users u ON u.id=r.buyer_user_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&state, &frozen))
		require.Equal(t, "voided", state)
		require.True(t, frozen.IsZero())
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_attempts WHERE owner_user_id=$1`, f.owner).Scan(&attempts))
		require.Zero(t, attempts)
	})
	t.Run("same owner cannot dispatch", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		_, err := db.Exec(`UPDATE users SET balance=5 WHERE id=$1`, f.owner)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO wallet_funding_states(user_id,migration_version,reconciled_at) VALUES($1,1,now())`, f.owner)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','selftest',$2,5,5)`, f.owner, uuid.NewString())
		require.NoError(t, err)
		require.NoError(t, db.QueryRow(`INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,$3,'self') RETURNING id`, f.owner, f.group, uuid.NewString()).Scan(&f.key))
		f.buyer = f.owner
		up := &realGatewayUpstream{status: 200, body: `{}`}
		rec := sendReal(f.router(t, up), "/v1/responses", `{"model":"public","input":"hi"}`, "")
		require.NotEqual(t, 200, rec.Code)
		require.Zero(t, up.count())
		var status string
		var frozen decimal.Decimal
		require.NoError(t, db.QueryRow(`SELECT r.status,u.frozen_balance FROM marketplace_requests r JOIN users u ON u.id=r.buyer_user_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&status, &frozen))
		require.Equal(t, "voided", status)
		require.True(t, frozen.IsZero())
	})
	t.Run("supplier slot is shared across handlers", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		up := &realGatewayUpstream{status: 200, body: `{"id":"resp","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4}}`, entered: make(chan struct{}, 2), release: make(chan struct{})}
		r1, r2 := f.router(t, up), f.router(t, up)
		first := make(chan *httptest.ResponseRecorder, 1)
		second := make(chan *httptest.ResponseRecorder, 1)
		sessionRequest := func(router http.Handler, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			req.Header.Set("session_id", "same-conversation")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			return rec
		}
		go func() { first <- sessionRequest(r1, `{"model":"public","input":"one"}`) }()
		select {
		case <-up.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("first request never dispatched")
		}
		defer func() {
			select {
			case <-up.release:
			default:
				close(up.release)
			}
		}()
		go func() { second <- sessionRequest(r2, `{"model":"public","input":"two"}`) }()
		require.Eventually(t, func() bool {
			var n int
			err := db.QueryRow(`SELECT count(*) FROM marketplace_requests WHERE buyer_user_id=$1`, f.buyer).Scan(&n)
			return err == nil && n == 2
		}, 3*time.Second, 20*time.Millisecond, "second request never reached reservation")
		slots, err := rdb.ZCard(ctx, fmt.Sprintf("concurrency:account:%d", f.account)).Result()
		require.NoError(t, err)
		require.Equal(t, int64(1), slots)
		live, err := rdb.Keys(ctx, "*live*").Result()
		require.NoError(t, err)
		require.Empty(t, live, "HTTP requests must not create conversation leases")
		select {
		case <-up.entered:
			t.Fatal("second request entered upstream while supplier slot held")
		case <-time.After(200 * time.Millisecond):
		}
		close(up.release)
		select {
		case r := <-first:
			require.Equal(t, 200, r.Code, r.Body.String())
		case <-time.After(5 * time.Second):
			t.Fatal("first request did not finish")
		}
		select {
		case r := <-second:
			if r.Code == 200 {
				require.Equal(t, 2, up.count())
			} else {
				require.Equal(t, 1, up.count())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("second request did not finish")
		}
		// The same conversation can start another request after completion;
		// a request slot must not turn into a retained conversation lease.
		previous := up.count()
		third := sessionRequest(r2, `{"model":"public","input":"three"}`)
		require.Equal(t, 200, third.Code, third.Body.String())
		require.Equal(t, previous+1, up.count())
		slots, err = rdb.ZCard(ctx, fmt.Sprintf("concurrency:account:%d", f.account)).Result()
		require.NoError(t, err)
		require.Zero(t, slots)
		var active int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_attempts WHERE owner_user_id=$1 AND status='possibly_executed'`, f.owner).Scan(&active))
		require.Zero(t, active)
	})

}
