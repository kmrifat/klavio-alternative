package cache

import (
	"context"
	"sync"
	"time"
)

// Limiter answers "may this key do one more thing in this window?".
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// Allow is a fixed-window counter in Redis: INCR, and set the expiry on the
// first hit. Cheap and good enough for abuse protection.
func (c *Client) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	k := "ratelimit:" + key
	n, err := c.Incr(ctx, k).Result()
	if err != nil {
		return true, err // fail open: never block an unsubscribe because Redis hiccuped
	}
	if n == 1 {
		c.Expire(ctx, k, window)
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
