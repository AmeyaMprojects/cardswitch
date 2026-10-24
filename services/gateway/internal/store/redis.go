package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLocker is a single-instance lock: SET NX with a TTL, released only by
// the holder (compare-and-delete in a Lua script, which Redis runs atomically).
type RedisLocker struct {
	rdb *redis.Client
}

func NewRedisLocker(addr string) *RedisLocker {
	return &RedisLocker{rdb: redis.NewClient(&redis.Options{Addr: addr})}
}

var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`)

func (l *RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (string, error) {
	buf := make([]byte, 16)
	rand.Read(buf)
	token := hex.EncodeToString(buf)
	ok, err := l.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil || !ok {
		return "", err
	}
	return token, nil
}

// Release deletes the lock only if we still own it. Without the check, a slow
// request whose lock expired could delete the lock of the request after it.
func (l *RedisLocker) Release(ctx context.Context, key, token string) error {
	return releaseScript.Run(ctx, l.rdb, []string{key}, token).Err()
}

func (l *RedisLocker) Close() error { return l.rdb.Close() }
