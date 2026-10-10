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
