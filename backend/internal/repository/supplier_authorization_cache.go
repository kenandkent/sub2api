package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"time"
)

const supplierAuthorizationTTL = 600 * time.Second

// SupplierAuthorizationCache shares authorization state across instances. Its keys and values are confidential.
type SupplierAuthorizationCache struct{ redis *redis.Client }

func NewSupplierAuthorizationCache(client *redis.Client) *SupplierAuthorizationCache {
	return &SupplierAuthorizationCache{redis: client}
}
func supplierAuthorizationKey(id string, user int64) string {
	return fmt.Sprintf("supplier:authorization:%d:%s", user, id)
}
func (c *SupplierAuthorizationCache) Put(ctx context.Context, id string, s service.SupplierAuthorizationSession) error {
	if id == "" || s.UserID <= 0 {
		return service.ErrSupplierAuthorizationInvalid
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ok, err := c.redis.SetNX(ctx, supplierAuthorizationKey(id, s.UserID), data, supplierAuthorizationTTL).Result()
	if err != nil {
		return err
	}
	if !ok {
		return service.ErrSupplierAuthorizationInvalid
	}
	return nil
}

var consumeSupplierAuthorization = redis.NewScript(`local v = redis.call('GET', KEYS[1]); if v then redis.call('DEL', KEYS[1]); end; return v`)

func (c *SupplierAuthorizationCache) Consume(ctx context.Context, id string, user int64) (service.SupplierAuthorizationSession, error) {
	var session service.SupplierAuthorizationSession
	if id == "" || user <= 0 {
		return session, service.ErrSupplierAuthorizationInvalid
	}
	data, err := consumeSupplierAuthorization.Run(ctx, c.redis, []string{supplierAuthorizationKey(id, user)}).Text()
	if err != nil {
		return session, service.ErrSupplierAuthorizationInvalid
	}
	if json.Unmarshal([]byte(data), &session) != nil || session.UserID != user {
		return service.SupplierAuthorizationSession{}, service.ErrSupplierAuthorizationInvalid
	}
	return session, nil
}
