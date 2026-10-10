package cache

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestConnectRejectsBadURL(t *testing.T) {
	if _, err := Connect(context.Background(), "http://not-redis"); err == nil {
		t.Fatal("expected an error for a non-redis URL")
	}
}

// Needs a real Redis: set TEST_REDIS_URL (make test-integration does).
func TestConnectAndRoundTrip(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	key := "test:cache:" + t.Name()
	if err := c.Set(ctx, key, "ok", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	defer c.Del(context.Background(), key)

	got, err := c.Get(ctx, key).Result()
	if err != nil || got != "ok" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestRedisLimiter(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx := context.Background()
	c, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := "test:" + t.Name() + time.Now().String()
	defer c.Del(ctx, "ratelimit:"+key)
	for i := 1; i <= 4; i++ {
		ok, err := c.Allow(ctx, key, 3, time.Minute)
		if err != nil || ok != (i <= 3) {
			t.Fatalf("hit %d: allowed=%v err=%v", i, ok, err)
		}
	}
	if ttl := c.TTL(ctx, "ratelimit:"+key).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("window not set: %v", ttl)
	}

	// A key that lost its expiry (crash, manual edit) gets one back on the next hit.
	stuck := key + ":stuck"
	defer c.Del(ctx, "ratelimit:"+stuck)
	c.Set(ctx, "ratelimit:"+stuck, 99, 0)
	if ok, _ := c.Allow(ctx, stuck, 3, time.Minute); ok {
		t.Fatal("over-limit key allowed")
	}
	if ttl := c.TTL(ctx, "ratelimit:"+stuck).Val(); ttl <= 0 {
		t.Fatalf("stuck key wasn't given an expiry: %v", ttl)
	}
}

func TestMemoryLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	m := &MemoryLimiter{Now: func() time.Time { return now }}
	for i := 1; i <= 3; i++ {
		if ok, _ := m.Allow(context.Background(), "k", 2, time.Minute); ok != (i <= 2) {
			t.Fatalf("hit %d", i)
		}
	}
	now = now.Add(61 * time.Second)
	if ok, _ := m.Allow(context.Background(), "k", 2, time.Minute); !ok {
		t.Fatal("window didn't reset")
	}
}
