package cache

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"
)

// Coordinator wraps Store with singleflight for miss coalescing.
type Coordinator struct {
	Store Store
	sf    singleflight.Group
}

func NewCoordinator(s Store) *Coordinator {
	return &Coordinator{Store: s}
}

// GetOrLoad returns cached bytes or runs load once per key.
// load should execute backend + return serialized CachedResult bytes.
// On Redis errors during GET, returns errPassthrough so caller fails open.
func (c *Coordinator) GetOrLoad(ctx context.Context, key string, load func(ctx context.Context) ([]byte, error)) ([]byte, bool, error) {
	if c == nil || c.Store == nil {
		b, err := load(ctx)
		return b, false, err
	}
	type result struct {
		b   []byte
		hit bool
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
		b, err := c.Store.Get(ctx, key)
		if err == nil {
			return result{b: b, hit: true}, nil
		}
		if errors.Is(err, ErrMiss) {
			// miss — fall through to load
		} else if errors.Is(err, ErrUnavailable) {
			return nil, err
		} else {
			return nil, err
		}
		loaded, lerr := load(ctx)
		if lerr != nil {
			return nil, lerr
		}
		return result{b: loaded, hit: false}, nil
	})
	if err != nil {
		return nil, false, err
	}
	r := v.(result)
	return r.b, r.hit, nil
}

// BestEffortSet stores value and registers key; never returns blocking errors to caller.
func (c *Coordinator) BestEffortSet(ctx context.Context, tenantID, connectionID, db, coll, key string, value []byte, ttl time.Duration) {
	if c == nil || c.Store == nil || len(value) == 0 {
		return
	}
	_ = c.Store.Set(ctx, key, value, ttl)
	_ = c.Store.RegisterKey(ctx, tenantID, connectionID, db, coll, key)
}

// BestEffortInvalidate runs connection-scoped namespace invalidation (fail-open for callers).
func (c *Coordinator) BestEffortInvalidate(ctx context.Context, tenantID, connectionID, db, coll string) error {
	if c == nil || c.Store == nil {
		return nil
	}
	return c.Store.InvalidateNamespace(ctx, tenantID, connectionID, db, coll)
}
