package cursor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
)

// State tracks a backend cursor exposed to a wire client via a synthetic ID.
type State struct {
	ID        int64
	TenantID  string
	ConnKey   string // connection identifier; cursors are scoped to a connection
	NS        string
	Cursor    *mongo.Cursor
	CreatedAt time.Time
	LastUsed  time.Time
}

// Registry maps client-visible cursor IDs to backend cursors.
// IDs are process-unique (atomic counter); scoped by connection key for isolation.
type Registry struct {
	mu      sync.Mutex
	byID    map[int64]*State
	nextID  atomic.Int64
	idleTTL time.Duration
}

func NewRegistry(idleTTL time.Duration) *Registry {
	if idleTTL <= 0 {
		idleTTL = 10 * time.Minute
	}
	r := &Registry{
		byID:    make(map[int64]*State),
		idleTTL: idleTTL,
	}
	r.nextID.Store(1)
	return r
}

// Register stores a backend cursor and returns the client-visible ID.
func (r *Registry) Register(tenantID, connKey, ns string, cur *mongo.Cursor) int64 {
	id := r.nextID.Add(1)
	now := time.Now()
	st := &State{
		ID:        id,
		TenantID:  tenantID,
		ConnKey:   connKey,
		NS:        ns,
		Cursor:    cur,
		CreatedAt: now,
		LastUsed:  now,
	}
	r.mu.Lock()
	r.byID[id] = st
	r.mu.Unlock()
	return id
}

// Get retrieves a cursor if it belongs to the given connection and tenant.
func (r *Registry) Get(id int64, tenantID, connKey string) (*State, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	if st.TenantID != tenantID || st.ConnKey != connKey {
		return nil, false
	}
	st.LastUsed = time.Now()
	return st, true
}

// Remove deletes and closes the backend cursor.
func (r *Registry) Remove(id int64, tenantID, connKey string) {
	r.mu.Lock()
	st, ok := r.byID[id]
	if ok && st.TenantID == tenantID && st.ConnKey == connKey {
		delete(r.byID, id)
	} else {
		ok = false
	}
	r.mu.Unlock()
	if ok && st.Cursor != nil {
		_ = st.Cursor.Close(context.Background())
	}
}

// KillMany removes multiple cursors (killCursors command).
func (r *Registry) KillMany(ids []int64, tenantID, connKey string) {
	for _, id := range ids {
		r.Remove(id, tenantID, connKey)
	}
}

// CleanupConn removes all cursors for a connection (client disconnect).
func (r *Registry) CleanupConn(connKey string) {
	r.mu.Lock()
	var toClose []*mongo.Cursor
	for id, st := range r.byID {
		if st.ConnKey == connKey {
			toClose = append(toClose, st.Cursor)
			delete(r.byID, id)
		}
	}
	r.mu.Unlock()
	for _, c := range toClose {
		if c != nil {
			_ = c.Close(context.Background())
		}
	}
}

// PruneIdle removes cursors not used within idleTTL. Call periodically.
func (r *Registry) PruneIdle() int {
	cutoff := time.Now().Add(-r.idleTTL)
	r.mu.Lock()
	var toClose []*mongo.Cursor
	for id, st := range r.byID {
		if st.LastUsed.Before(cutoff) {
			toClose = append(toClose, st.Cursor)
			delete(r.byID, id)
		}
	}
	r.mu.Unlock()
	for _, c := range toClose {
		if c != nil {
			_ = c.Close(context.Background())
		}
	}
	return len(toClose)
}

// Count returns open cursor entries.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}
