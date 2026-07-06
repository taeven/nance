package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrMiss indicates the key was not found in cache.
var ErrMiss = errors.New("cache miss")

// ErrUnavailable indicates Redis is down or timed out (fail-open).
var ErrUnavailable = errors.New("cache unavailable")

// Store is the minimal cache interface used by the proxy.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// RegisterKey adds key to the connection-scoped namespace registry (for invalidation).
	RegisterKey(ctx context.Context, tenantID, connectionID, db, coll, key string) error
	InvalidateNamespace(ctx context.Context, tenantID, connectionID, db, coll string) error
	Ping(ctx context.Context) error
	Close() error
}

// RedisStore implements Store with go-redis (single instance or cluster via Addr).
type RedisStore struct {
	client redis.UniversalClient
	getTO  time.Duration
	setTO  time.Duration
}

// Options configures the Redis client.
type Options struct {
	Addr       string // host:port or comma-separated for cluster
	Password   string
	DB         int
	GetTimeout time.Duration
	SetTimeout time.Duration
}

// NewRedisStore dials Redis. Returns a store even if ping fails (fail-open at runtime).
func NewRedisStore(ctx context.Context, opts Options) (*RedisStore, error) {
	if opts.Addr == "" {
		return nil, errors.New("redis addr required")
	}
	if opts.GetTimeout <= 0 {
		opts.GetTimeout = 50 * time.Millisecond
	}
	if opts.SetTimeout <= 0 {
		opts.SetTimeout = 200 * time.Millisecond
	}
	client := redis.NewClient(&redis.Options{
		Addr:     opts.Addr,
		Password: opts.Password,
		DB:       opts.DB,
	})
	s := &RedisStore{client: client, getTO: opts.GetTimeout, setTO: opts.SetTimeout}
	// Non-fatal ping on startup
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = client.Ping(pctx).Err()
	return s, nil
}

func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, s.getTO)
	defer cancel()
	val, err := s.client.Get(cctx, key).Bytes()
	if err == redis.Nil {
		return nil, ErrMiss
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return val, nil
}

func (s *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, s.setTO)
	defer cancel()
	if err := s.client.Set(cctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func registryKey(tenantID, connectionID, db, coll string) string {
	// Hash tag keeps tenant keys on same cluster slot.
	if connectionID == "" {
		connectionID = "_"
	}
	return fmt.Sprintf("nance:tenant:{%s}:conn:%s:ns:%s.%s:known_keys", tenantID, connectionID, db, coll)
}

func (s *RedisStore) RegisterKey(ctx context.Context, tenantID, connectionID, db, coll, key string) error {
	cctx, cancel := context.WithTimeout(ctx, s.setTO)
	defer cancel()
	rk := registryKey(tenantID, connectionID, db, coll)
	return s.client.SAdd(cctx, rk, key).Err()
}

func (s *RedisStore) InvalidateNamespace(ctx context.Context, tenantID, connectionID, db, coll string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rk := registryKey(tenantID, connectionID, db, coll)
	members, err := s.client.SMembers(cctx, rk).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if len(members) == 0 {
		_ = s.client.Del(cctx, rk).Err()
		return nil
	}
	pipe := s.client.Pipeline()
	for _, m := range members {
		pipe.Del(cctx, m)
	}
	pipe.Del(cctx, rk)
	_, err = pipe.Exec(cctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *RedisStore) Close() error {
	return s.client.Close()
}

// MemoryStore is an in-process cache for unit tests (no Redis required).
type MemoryStore struct {
	mu   map[string]memEntry
	sets map[string]map[string]struct{}
}

type memEntry struct {
	val []byte
	exp time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		mu:   make(map[string]memEntry),
		sets: make(map[string]map[string]struct{}),
	}
}

func (m *MemoryStore) Get(_ context.Context, key string) ([]byte, error) {
	e, ok := m.mu[key]
	if !ok || (!e.exp.IsZero() && time.Now().After(e.exp)) {
		if ok {
			delete(m.mu, key)
		}
		return nil, ErrMiss
	}
	out := make([]byte, len(e.val))
	copy(out, e.val)
	return out, nil
}

func (m *MemoryStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	cp := make([]byte, len(value))
	copy(cp, value)
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.mu[key] = memEntry{val: cp, exp: exp}
	return nil
}

func (m *MemoryStore) RegisterKey(_ context.Context, tenantID, connectionID, db, coll, key string) error {
	rk := registryKey(tenantID, connectionID, db, coll)
	if m.sets[rk] == nil {
		m.sets[rk] = make(map[string]struct{})
	}
	m.sets[rk][key] = struct{}{}
	return nil
}

func (m *MemoryStore) InvalidateNamespace(_ context.Context, tenantID, connectionID, db, coll string) error {
	rk := registryKey(tenantID, connectionID, db, coll)
	for k := range m.sets[rk] {
		delete(m.mu, k)
	}
	delete(m.sets, rk)
	return nil
}

func (m *MemoryStore) Ping(_ context.Context) error { return nil }
func (m *MemoryStore) Close() error                 { return nil }

// NoopStore always misses / ignores writes (cache disabled).
type NoopStore struct{}

func (NoopStore) Get(context.Context, string) ([]byte, error) { return nil, ErrUnavailable }
func (NoopStore) Set(context.Context, string, []byte, time.Duration) error {
	return ErrUnavailable
}
func (NoopStore) RegisterKey(context.Context, string, string, string, string, string) error {
	return ErrUnavailable
}
func (NoopStore) InvalidateNamespace(context.Context, string, string, string, string) error {
	return ErrUnavailable
}
func (NoopStore) Ping(context.Context) error { return ErrUnavailable }
func (NoopStore) Close() error               { return nil }
