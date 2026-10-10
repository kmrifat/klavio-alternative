// Package cache owns the Redis connection (rate limits, short-lived caches).
package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Client wraps go-redis so callers get a Ping that matches server.Pinger.
type Client struct {
	*redis.Client
}

func (c *Client) Ping(ctx context.Context) error {
	return c.Client.Ping(ctx).Err()
}

// Connect parses a redis:// URL, opens a client and verifies Redis is reachable.
func Connect(ctx context.Context, url string) (*Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	c := &Client{redis.NewClient(opts)}
	if err := c.Ping(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return c, nil
}
