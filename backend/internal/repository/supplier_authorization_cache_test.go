package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupplierAuthorizationCacheOneTimeAndExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	a := NewSupplierAuthorizationCache(client)
	b := NewSupplierAuthorizationCache(client)
	ctx := context.Background()
	session := service.SupplierAuthorizationSession{UserID: 7, State: "secret-state", CodeVerifier: "secret-verifier"}
	if err := a.Put(ctx, "session", session); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Consume(ctx, "session", 8); err == nil {
		t.Fatal("wrong user accepted")
	}
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Consume(ctx, "session", 7); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("successful consumes=%d", wins.Load())
	}
	if err := a.Put(ctx, "expired", session); err != nil {
		t.Fatal(err)
	}
	server.FastForward(601 * time.Second)
	if _, err := b.Consume(ctx, "expired", 7); err == nil {
		t.Fatal("expired session accepted")
	}
}
