package auth

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/redis/go-redis/v9"
)

// A ceremony is one begin → finish passkey exchange. Its challenge must be kept
// server-side between the two calls and used at most once.
const ceremonyTTL = 5 * time.Minute

var errCeremonyNotFound = errors.New("ceremony not found or expired")

type ceremony struct {
	Session webauthn.SessionData `json:"session"`
	// Kind guards against using a login challenge to finish a registration, etc.
	Kind string `json:"kind"`
	// Ref ties the ceremony to an invite or session (empty for login).
	Ref string `json:"ref"`
}

type CeremonyStore interface {
	Put(ctx context.Context, id string, c ceremony) error
	// Take returns the ceremony and deletes it, so a challenge is used once.
	Take(ctx context.Context, id string) (ceremony, error)
}

// RedisCeremonies keeps ceremonies in Redis with a TTL.
type RedisCeremonies struct{ R *redis.Client }

func (s RedisCeremonies) Put(ctx context.Context, id string, c ceremony) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.R.Set(ctx, "webauthn:ceremony:"+id, raw, ceremonyTTL).Err()
}

func (s RedisCeremonies) Take(ctx context.Context, id string) (ceremony, error) {
	raw, err := s.R.GetDel(ctx, "webauthn:ceremony:"+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return ceremony{}, errCeremonyNotFound
	}
	if err != nil {
		return ceremony{}, err
	}
	var c ceremony
	return c, json.Unmarshal(raw, &c)
}

// MemoryCeremonies is for tests.
type MemoryCeremonies struct {
	mu sync.Mutex
	m  map[string]ceremony
}

func (s *MemoryCeremonies) Put(_ context.Context, id string, c ceremony) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]ceremony{}
	}
	s.m[id] = c
	return nil
}

func (s *MemoryCeremonies) Take(_ context.Context, id string) (ceremony, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[id]
	if !ok {
		return ceremony{}, errCeremonyNotFound
	}
	delete(s.m, id)
	return c, nil
}
