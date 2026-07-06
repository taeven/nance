package service

import (
	"context"

	cpstore "github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/proxy/cache"
)

// RedisInvalidator adapts proxy/cache.Store for control-plane explicit invalidation.
// When Connections is set, namespace invalidation runs for every connection under the tenant.
type RedisInvalidator struct {
	Store       cache.Store
	Connections cpstore.Store // optional; used to list connection IDs for tenant-wide flush
}

func (r *RedisInvalidator) InvalidateNamespace(ctx context.Context, tenantID, db, coll string) error {
	if r == nil || r.Store == nil {
		return nil
	}
	if r.Connections != nil {
		list, err := r.Connections.ListConnections(ctx, tenantID)
		if err != nil {
			return err
		}
		for _, c := range list {
			if err := r.Store.InvalidateNamespace(ctx, tenantID, c.ID, db, coll); err != nil {
				return err
			}
		}
		return nil
	}
	// Fallback: connection-less registry key (legacy / tests)
	return r.Store.InvalidateNamespace(ctx, tenantID, "", db, coll)
}

func (r *RedisInvalidator) InvalidateTags(ctx context.Context, tenantID string, tags []string) error {
	if r == nil || r.Store == nil {
		return nil
	}
	return cache.InvalidateTags(ctx, r.Store, tenantID, tags)
}
