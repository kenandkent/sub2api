//go:build unit && integration

package handler

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestMarketplaceReviewRegressions(t *testing.T) {
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
	const reply = `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4}}`
	t.Run("cancelled client retains supplier slot until upstream returns", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		up := &realGatewayUpstream{status: 200, body: reply, entered: make(chan struct{}, 2), release: make(chan struct{})}
		router := f.router(t, up)
		requestCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{}, 2)
		started := 1
		defer func() {
			close(up.release)
			for i := 0; i < started; i++ {
				<-done
			}
		}()
		go func() {
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"public","input":"one"}`)).WithContext(requestCtx)
			router.ServeHTTP(httptest.NewRecorder(), req)
			done <- struct{}{}
		}()
		select {
		case <-up.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("first never reached upstream")
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
		n, err := rdb.ZCard(ctx, fmt.Sprintf("concurrency:account:%d", f.account)).Result()
		require.NoError(t, err)
		if n != 1 {
			t.Errorf("cancel released supplier slot while upstream remained active: slots=%d", n)
		}
		started = 2
		go func() { sendReal(router, "/v1/responses", `{"model":"public","input":"two"}`, ""); done <- struct{}{} }()
		select {
		case <-up.entered:
			t.Errorf("second upstream entered while first remains active; account concurrency=1")
		case <-time.After(3 * time.Second):
		}
		up.mu.Lock()
		peak := up.peak[f.account]
		active := up.active[f.account]
		up.mu.Unlock()
		t.Logf("active=%d peak=%d", active, peak)
		if peak > 1 {
			t.Errorf("concurrency exceeded: peak=%d", peak)
		}
	})
	t.Run("execution deadline returns slot and reviews without replay", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		f.cfg.Marketplace.UpstreamTimeoutSeconds = 1
		up := &realGatewayUpstream{status: 200, body: reply, entered: make(chan struct{}, 1), release: make(chan struct{}), honorContext: true}
		rec := sendReal(f.router(t, up), "/v1/responses", `{"model":"public","input":"hi"}`, "")
		var status string
		var frozen decimal.Decimal
		require.NoError(t, db.QueryRow(`SELECT r.status,u.frozen_balance FROM marketplace_requests r JOIN users u ON u.id=r.buyer_user_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&status, &frozen))
		slots, err := rdb.ZCard(ctx, fmt.Sprintf("concurrency:account:%d", f.account)).Result()
		require.NoError(t, err)
		require.Equal(t, 502, rec.Code)
		require.Equal(t, "needs_review", status)
		require.True(t, frozen.IsPositive())
		require.Equal(t, 1, up.count())
		require.Zero(t, slots)
	})

	t.Run("slow reader cannot pin a supplier slot", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		f.cfg.Marketplace.UpstreamTimeoutSeconds = 1
		event := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 2048) + "\"}\n\n"
		up := &realGatewayUpstream{status: 200, contentType: "text/event-stream", body: strings.Repeat(event, 16384)}
		inner := f.router(t, up)
		outer := gin.New()
		outer.Use(OpsErrorLoggerMiddleware(nil))
		outer.POST("/v1/responses", func(c *gin.Context) { inner.ServeHTTP(c.Writer, c.Request) })
		done := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); outer.ServeHTTP(w, r) }))
		defer server.Close()
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		require.NoError(t, err)
		defer conn.Close()
		body := `{"model":"public","input":"hi","stream":true}`
		_, err = fmt.Fprintf(conn, "POST /v1/responses HTTP/1.1\r\nHost: local\r\nContent-Type: application/json\r\nX-Require-Network-Deadline: true\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		require.NoError(t, err)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("unread downstream blocked handler beyond bounded execution")
		}
		slots, err := rdb.ZCard(ctx, fmt.Sprintf("concurrency:account:%d", f.account)).Result()
		require.NoError(t, err)
		if slots != 0 {
			t.Errorf("slow client retained supplier slot: %d", slots)
		}
	})

	t.Run("ops wrapped shared response clears deadline for keepalive", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		f.cfg.Marketplace.UpstreamTimeoutSeconds = 1
		up := &realGatewayUpstream{status: 200, body: reply}
		inner := f.router(t, up)
		outer := gin.New()
		outer.Use(OpsErrorLoggerMiddleware(nil))
		outer.POST("/v1/responses", func(c *gin.Context) { inner.ServeHTTP(c.Writer, c.Request) })
		outer.GET("/plain", func(c *gin.Context) { c.String(200, "ordinary") })
		server := httptest.NewServer(outer)
		defer server.Close()
		transport := &http.Transport{MaxConnsPerHost: 1}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		first, err := http.NewRequest("POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"public","input":"hi"}`))
		require.NoError(t, err)
		first.Header.Set("X-Require-Network-Deadline", "true")
		resp, err := client.Do(first)
		require.NoError(t, err)
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode, string(b))
		require.Equal(t, 1, up.count())
		time.Sleep(1200 * time.Millisecond)
		reused := false
		req, err := http.NewRequest("GET", server.URL+"/plain", nil)
		require.NoError(t, err)
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
		second, err := client.Do(req)
		require.NoError(t, err)
		defer second.Body.Close()
		b, err = io.ReadAll(second.Body)
		require.NoError(t, err)
		require.True(t, reused, "test must reuse the original connection")
		require.Equal(t, 200, second.StatusCode)
		require.Equal(t, "ordinary", string(b))
	})

	t.Run("context bound is enforced", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		// Provider attests a 4000 token hard ceiling while the catalog reserves
		// for only 1000. Admission must fail before any reservation or call.
		f.cfg.Marketplace.Providers[0].MaxBillableInputTokensByModel["gpt-5.4"] = 4000
		up := &realGatewayUpstream{status: 200, body: `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":4000,"output_tokens":4}}`}
		body := `{"model":"public","input":"` + strings.Repeat("test ", 6000) + `"}`
		rec := sendReal(f.router(t, up), "/v1/responses", body, "")
		var receipts int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_requests WHERE buyer_user_id=$1`, f.buyer).Scan(&receipts))
		t.Logf("HTTP=%d calls=%d receipts=%d", rec.Code, up.count(), receipts)
		if up.count() != 0 || receipts != 0 {
			t.Errorf("over-limit input was dispatched or reserved: calls=%d receipts=%d", up.count(), receipts)
		}
	})
	t.Run("malformed chat is rejected without frozen funds", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		up := &realGatewayUpstream{status: 200, body: reply}
		rec := sendReal(f.router(t, up), "/v1/chat/completions", `{"model":"public","messages":"invalid"}`, "")
		var frozen decimal.Decimal
		var attempts int
		require.NoError(t, db.QueryRow(`SELECT frozen_balance FROM users WHERE id=$1`, f.buyer).Scan(&frozen))
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM marketplace_attempts a JOIN marketplace_requests r ON r.id=a.receipt_id WHERE r.buyer_user_id=$1`, f.buyer).Scan(&attempts))
		t.Logf("HTTP=%d calls=%d frozen=%s attempts=%d body=%s", rec.Code, up.count(), frozen, attempts, rec.Body.String())
		if rec.Code != http.StatusBadRequest || !frozen.IsZero() || attempts != 0 {
			t.Errorf("pre-send malformed request retained funds or created an attempt")
		}
	})
	t.Run("oversized response is delivered or returns explicit failure", func(t *testing.T) {
		f := newRealGatewayFixture(t, db, rdb)
		giant := strings.Repeat("x", (8<<20)+1000)
		body := `{"id":"resp_test","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + giant + `"}]}],"usage":{"input_tokens":10,"output_tokens":4}}`
		up := &realGatewayUpstream{status: 200, body: body}
		rec := sendReal(f.router(t, up), "/v1/responses", `{"model":"public","input":"hi"}`, "")
		var status string
		var charge decimal.Decimal
		require.NoError(t, db.QueryRow(`SELECT status,actual_charge FROM marketplace_requests WHERE buyer_user_id=$1`, f.buyer).Scan(&status, &charge))
		t.Logf("HTTP=%d response_bytes=%d status=%s charge=%s calls=%d", rec.Code, rec.Body.Len(), status, charge, up.count())
		if rec.Code == http.StatusOK && rec.Body.Len() == 0 && charge.IsPositive() {
			t.Errorf("charged for empty 200 response")
		}
	})
}
