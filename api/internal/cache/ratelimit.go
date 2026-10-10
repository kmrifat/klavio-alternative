package cache

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter answers "may this key do one more thing in this window?".
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// allowScript counts a hit and makes sure the key expires, in one atomic step.
// The TTL check also repairs a key that somehow lost its expiry, so nobody can
// end up blocked forever.
var allowScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return n`)

// Allow is a fixed-window counter in Redis. Cheap and good enough for abuse protection.
func (c *Client) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	n, err := allowScript.Run(ctx, c.Client, []string{"ratelimit:" + key}, window.Milliseconds()).Int64()
	if err != nil {
		return true, err // fail open: never block an unsubscribe because Redis hiccuped
	}
	return n <= int64(limit), nil
}

// MemoryLimiter is the in-process version, for tests.
type MemoryLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	Now  func() time.Time
}

func (m *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	if m.hits == nil {
		m.hits = map[string][]time.Time{}
	}
	kept := m.hits[key][:0]
	for _, t := range m.hits[key] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		m.hits[key] = kept
		return false, nil
	}
	m.hits[key] = append(kept, now)
	return true, nil
}
