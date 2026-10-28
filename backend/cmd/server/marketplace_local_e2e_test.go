//go:build unit && integration && local_e2e

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
)

// Runs the real server binary under a macOS network sandbox against disposable
// local PostgreSQL, Redis and an OpenAI-shaped upstream. No provider calls are made.
// Build it first from backend with:
//
//	../.superpowers/bin/go build -o ../.superpowers/live-e2e/sub2api ./cmd/server
//
// Then run with -tags='unit integration local_e2e'. Set MARKETPLACE_E2E_NORMAL_ORDER=1
// to exercise the opposite request order.
func TestMarketplaceLocalServerE2E(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("local E2E requires macOS sandbox-exec")
	}
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	ctx := context.Background()
	pg, err := pgcontainer.Run(ctx, "postgres:18.1-alpine3.23", pgcontainer.WithDatabase("smoke"), pgcontainer.WithUsername("smoke"), pgcontainer.WithPassword("localfixture"), pgcontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := pg.Terminate(ctx); err != nil {
			t.Errorf("terminate local PostgreSQL: %v", err)
		}
	})
	rc, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := rc.Terminate(ctx); err != nil {
			t.Errorf("terminate local Redis: %v", err)
		}
	})
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close PostgreSQL: %v", err)
		}
	})
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	_, err = db.Exec(`INSERT INTO settings(key,value) VALUES($1,'false'),($2,'false') ON CONFLICT(key) DO UPDATE SET value='false'`, service.SettingKeyOpenAICodexVersionAutoSyncEnabled, service.SettingKeyClaudeCodeVersionAutoSyncEnabled)
	require.NoError(t, err)
	pgAddr, err := pg.Endpoint(ctx, "")
	require.NoError(t, err)
	redisAddr, err := rc.Endpoint(ctx, "")
	require.NoError(t, err)
	pgHost, pgPort, err := net.SplitHostPort(pgAddr)
	require.NoError(t, err)
	redisHost, redisPort, err := net.SplitHostPort(redisAddr)
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("close Redis: %v", err)
		}
	})
	var mu sync.Mutex
	var calls int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read fake upstream request: %v", err)
			http.Error(w, "invalid upstream request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer fake-provider-key" {
			http.Error(w, "bad upstream request", 400)
			return
		}
		var req map[string]any
		if json.Unmarshal(body, &req) != nil || req["model"] != "mock-text" {
			http.Error(w, "bad upstream body", 400)
			return
		}
		output := `[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}]`
		if _, ok := req["tools"]; ok {
			output = `[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed"}]`
		}
		response := fmt.Sprintf(`{"id":"resp_fixture","object":"response","model":"mock-text","status":"completed","output":%s,"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}`, output)
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			if _, err := fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response); err != nil {
				t.Errorf("write fake streaming response: %v", err)
			}
		} else {
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprint(w, response); err != nil {
				t.Errorf("write fake response: %v", err)
			}
		}
	}))
	t.Cleanup(fake.Close)
	root, err := filepath.Abs("../../../.superpowers/live-e2e")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0700))
	data, err := os.MkdirTemp(root, "run-data-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := os.RemoveAll(data); err != nil {
			t.Errorf("remove local test data: %v", err)
		}
	})
	tmp := filepath.Join(data, "tmp")
	require.NoError(t, os.MkdirAll(tmp, 0700))
	profile := fmt.Sprintf("(version 1)\n(allow default)\n(deny network-outbound)\n(allow network-outbound (remote ip \"localhost:*\"))\n(deny file-write*)\n(allow file-write* (subpath %s))\n", strconv.Quote(data))
	profilePath := filepath.Join(data, "offline.sb")
	require.NoError(t, os.WriteFile(profilePath, []byte(profile), 0600))
	key := bytes.Repeat([]byte{3}, 32)
	cfg := fmt.Sprintf(`security:
  url_allowlist:
    enabled: false
    allow_insecure_http: true
pricing:
  remote_url: ""
  hash_url: ""
  data_dir: %s
  fallback_file: %s
  override_file: ""
plugins:
  data_dir: %s
log:
  output:
    to_stdout: true
    to_file: false
    file_path: %s
marketplace:
  enabled: true
  dispatch_enabled: true
  fee_bps: 1000
  hold_hours: 0
  max_supplier_concurrency: 1
  minimum_payout_usd: '0.00'
  credential_key_id: fixture
  credential_keys_json: '{"fixture":"%s"}'
  providers:
    - platform: openai
      label: Local fixture
      base_url: %s/v1
      account_types: [apikey]
      models: [mock-text]
`, strconv.Quote(filepath.Join(data, "pricing")), strconv.Quote(filepath.Join(data, "fallback.json")), strconv.Quote(filepath.Join(data, "plugins")), strconv.Quote(filepath.Join(data, "app.log")), base64.StdEncoding.EncodeToString(key), fake.URL)
	configPath := filepath.Join(data, "e2e-config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listenAddr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := listenAddr.Port
	require.NoError(t, listener.Close())
	logFile, err := os.Create(filepath.Join(data, "server.log"))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := logFile.Close(); err != nil {
			t.Errorf("close server log: %v", err)
		}
	})
	cmd := exec.Command("/usr/bin/sandbox-exec", "-f", profilePath, filepath.Join(root, "sub2api"))
	cmd.Dir = data
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"DATA_DIR=" + data, "TMPDIR=" + tmp, "CONFIG_FILE=" + configPath,
		"AUTO_SETUP=true", "SERVER_HOST=127.0.0.1", fmt.Sprintf("SERVER_PORT=%d", port), "SERVER_MODE=release",
		"DATABASE_HOST=" + pgHost, "DATABASE_PORT=" + pgPort, "DATABASE_USER=smoke", "DATABASE_PASSWORD=localfixture", "DATABASE_DBNAME=smoke", "DATABASE_SSLMODE=disable",
		"REDIS_HOST=" + redisHost, "REDIS_PORT=" + redisPort,
		"ADMIN_EMAIL=fixture-admin@example.test", "ADMIN_PASSWORD=FixturePassword123!",
		"JWT_SECRET=fixture-secret-0123456789-abcdef01234567", "TOTP_ENCRYPTION_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("stop server: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Errorf("wait for server: %v", err)
			}
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second}
	require.Eventually(t, func() bool {
		resp, e := client.Get(base + "/health")
		if e != nil {
			return false
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close health response: %v", err)
			return false
		}
		return resp.StatusCode == 200
	}, 45*time.Second, 250*time.Millisecond, "server failed to start under local sandbox")
	var admin, buyer, owner, account int64
	require.NoError(t, db.QueryRow(`SELECT id FROM users WHERE role='admin' LIMIT 1`).Scan(&admin))
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash,balance,concurrency) VALUES('buyer@example.test','x',5,10) RETURNING id`).Scan(&buyer))
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash) VALUES('supplier@example.test','x') RETURNING id`).Scan(&owner))
	_, err = db.Exec(`INSERT INTO wallet_funding_states(user_id,migration_version,reconciled_at) VALUES($1,1,now())`, buyer)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO wallet_funding_lots(user_id,kind,origin_type,origin_id,original,remaining) VALUES($1,'cash_paid','e2e','fixture',5,5)`, buyer)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO supplier_profiles(user_id,status) VALUES($1,'active')`, owner)
	require.NoError(t, err)
	groups := map[string]int64{}
	keys := map[string]string{}
	for _, protocol := range []string{"responses", "chat_completions"} {
		var group int64
		require.NoError(t, db.QueryRow(`INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id`, "e2e-"+protocol).Scan(&group))
		groups[protocol] = group
		keys[protocol] = "fixture-consumer-key-" + protocol
		_, err = db.Exec(`INSERT INTO api_keys(user_id,group_id,key,name) VALUES($1,$2,$3,'e2e')`, buyer, group, keys[protocol])
		require.NoError(t, err)
	}
	require.NoError(t, db.QueryRow(`INSERT INTO accounts(name,platform,type,owner_user_id,concurrency,extra) VALUES('local-supplier','openai','apikey',$1,1,'{"openai_responses_supported":true}') RETURNING id`, owner).Scan(&account))
	envelope, err := service.NewCredentialEnvelope("fixture", map[string][]byte{"fixture": key})
	require.NoError(t, err)
	sealed, err := envelope.Seal("openai", owner, account, []byte(`{"api_key":"fake-provider-key"}`))
	require.NoError(t, err)
	encoded, err := json.Marshal(sealed)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE accounts SET credentials=jsonb_build_object('_supplier_envelope',$1::jsonb) WHERE id=$2`, string(encoded), account)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sharing_offers(account_id,owner_user_id,review_status,enabled,allowed_models) VALUES($1,$2,'approved',true,'["mock-text"]')`, account, owner)
	require.NoError(t, err)
	for _, group := range groups {
		_, err = db.Exec(`INSERT INTO account_groups(account_id,group_id) VALUES($1,$2)`, account, group)
		require.NoError(t, err)
	}
	rate := decimal.NewFromInt(1)
	input := service.Money{Decimal: decimal.NewFromInt(2)}
	output := service.Money{Decimal: decimal.NewFromInt(4)}
	cache := service.Money{Decimal: decimal.NewFromInt(1)}
	book := service.MarketplacePriceBook{}
	for _, protocol := range []string{"responses", "chat_completions"} {
		book.Groups = append(book.Groups, service.MarketplaceCatalogGroup{GroupID: groups[protocol], Name: "e2e-" + protocol, Platform: "openai", Protocol: protocol, Models: []service.MarketplaceCatalogModel{{PublicModel: "mock-text", ActualModel: "mock-text", ContextLimit: 1000, MaxOutputTokens: 200, RateMultiplier: &rate, Tiers: []service.MarketplacePriceTier{{InputPerMillion: &input, OutputPerMillion: &output, CacheReadPerMillion: &cache, CacheWritePerMillion: &input}}}}})
	}
	_, err = service.NewMarketplaceCatalogService(repository.NewMarketplaceCatalogRepository(db)).Publish(ctx, admin, service.MarketplacePolicyVersion{FeeBPS: 1000, MaxSupplierConcurrency: 1, MinimumPayoutUSD: service.Money{Decimal: decimal.Zero}}, book)
	require.NoError(t, err)
	unauthorized, err := client.Post(base+"/v1/responses", "application/json", strings.NewReader(`{"model":"mock-text","input":"hi"}`))
	require.NoError(t, err)
	require.NoError(t, unauthorized.Body.Close())
	require.Equal(t, http.StatusUnauthorized, unauthorized.StatusCode)

	cases := []struct{ name, path, protocol, body string }{
		{"responses tools", "/v1/responses", "responses", `{"model":"mock-text","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`},
		{"responses stream", "/v1/responses", "responses", `{"model":"mock-text","input":"hi","stream":true}`},
		{"responses nonstream", "/v1/responses", "responses", `{"model":"mock-text","input":"hi"}`},
		{"chat nonstream", "/v1/chat/completions", "chat_completions", `{"model":"mock-text","messages":[{"role":"user","content":"hi"}]}`},
		{"chat stream", "/v1/chat/completions", "chat_completions", `{"model":"mock-text","messages":[{"role":"user","content":"hi"}],"stream":true}`},
		{"chat tools", "/v1/chat/completions", "chat_completions", `{"model":"mock-text","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`},
	}
	if os.Getenv("MARKETPLACE_E2E_NORMAL_ORDER") == "1" {
		cases[0], cases[2] = cases[2], cases[0]
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", base+tc.path, strings.NewReader(tc.body))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+keys[tc.protocol])
			req.Header.Set("Content-Type", "application/json")
			if i == 2 {
				payload, err := rdb.Get(ctx, fmt.Sprintf("sched:meta:%d", account)).Bytes()
				require.NoError(t, err)
				var metadata map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(payload, &metadata))
				ownerValue, present := metadata["OwnerUserID"]
				require.True(t, present, "warmed scheduler metadata has an owner field")
				require.JSONEq(t, fmt.Sprint(owner), string(ownerValue), "warmed scheduler metadata retains supplier identity")
			}
			resp, err := client.Do(req)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			require.NoError(t, err)
			require.NoError(t, closeErr)
			require.Equal(t, 200, resp.StatusCode, string(body))
			if strings.Contains(tc.name, "tools") {
				require.Contains(t, string(body), "lookup")
			}
		})
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	require.Equal(t, 6, gotCalls)
	var requests, attempts, captures, unbalanced, positiveCaptures, holds int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_requests WHERE buyer_user_id=$1 AND status='settled'`, buyer).Scan(&requests))
	require.Equal(t, 6, requests)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_attempts a JOIN marketplace_requests r ON r.id=a.receipt_id WHERE r.buyer_user_id=$1 AND a.account_id=$2 AND a.id=r.billable_attempt_id`, buyer, account).Scan(&attempts))
	require.Equal(t, 6, attempts)
	require.NoError(t, db.QueryRow(`SELECT count(*),count(*) FILTER (WHERE c.consumer_charge<>c.supplier_net+c.platform_fee) FROM marketplace_captures c JOIN marketplace_requests r ON r.id=c.receipt_id WHERE r.buyer_user_id=$1`, buyer).Scan(&captures, &unbalanced))
	require.Equal(t, 6, captures)
	require.Zero(t, unbalanced)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_captures c JOIN marketplace_requests r ON r.id=c.receipt_id WHERE r.buyer_user_id=$1 AND c.consumer_charge>0 AND c.supplier_net>0 AND c.platform_fee>0`, buyer).Scan(&positiveCaptures))
	require.Equal(t, 6, positiveCaptures)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM wallet_funding_operations WHERE user_id=$1 AND operation_type='hold'`, buyer).Scan(&holds))
	require.Equal(t, 6, holds)
	var frozen decimal.Decimal
	require.NoError(t, db.QueryRow(`SELECT frozen_balance FROM users WHERE id=$1`, buyer).Scan(&frozen))
	require.True(t, frozen.IsZero())
	slots, err := rdb.ZCard(ctx, fmt.Sprintf("concurrency:account:%d", account)).Result()
	require.NoError(t, err)
	require.Zero(t, slots)
}
