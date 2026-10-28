package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var releaseSupplierRefreshLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
 return redis.call('DEL', KEYS[1])
end
return 0`)

// Supplier refresh locks share the existing namespace, but release is scoped
// to a random owner so a stale process cannot remove a successor's lease.
func (c *geminiTokenCache) AcquireSupplierRefreshLock(ctx context.Context, cacheKey, owner string, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil || cacheKey == "" || owner == "" || ttl <= 0 {
		return false, service.ErrSupplierRuntimeUnavailable
	}
	return c.rdb.SetNX(ctx, oauthRefreshLockKeyPrefix+cacheKey, owner, ttl).Result()
}
func (c *geminiTokenCache) ReleaseSupplierRefreshLock(ctx context.Context, cacheKey, owner string) error {
	if c == nil || c.rdb == nil || cacheKey == "" || owner == "" {
		return service.ErrSupplierRuntimeUnavailable
	}
	return releaseSupplierRefreshLockScript.Run(ctx, c.rdb, []string{oauthRefreshLockKeyPrefix + cacheKey}, owner).Err()
}
