package pool

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/crypto"
	proxyconfig "github.com/taeven/nance/accelerator/internal/proxy/config"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"golang.org/x/sync/singleflight"
)

// tenantClient is one backend mongo.Client with idle / in-use tracking for eviction.
type tenantClient struct {
	client   *mongo.Client
	lastUsed time.Time
	refs     int // outstanding Get() without Release(); must be 0 to evict
}

// Manager maintains one mongo.Client per tenant, created lazily, with automatic
// eviction of idle clients (no active refs and lastUsed older than idle timeout).
type Manager struct {
	store  store.Store
	crypto *crypto.Config
	cfg    *proxyconfig.Config
	log    *slog.Logger

	mu      sync.Mutex
	clients map[string]*tenantClient
	sf      singleflight.Group

	// idleTimeout: close tenant clients unused for this long (0 = disable eviction).
	idleTimeout time.Duration
	// evictEvery: how often the reaper runs.
	evictEvery time.Duration

	stopOnce sync.Once
	stopCh   chan struct{}
	stopped  chan struct{}
}

func NewManager(s store.Store, c *crypto.Config, cfg *proxyconfig.Config, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	idle := 15 * time.Minute
	every := 1 * time.Minute
	if cfg != nil {
		// cfg.BackendIdleTimeout is fully authoritative (0 disables eviction).
		idle = cfg.BackendIdleTimeout
		if cfg.BackendIdleEvictInterval > 0 {
			every = cfg.BackendIdleEvictInterval
		}
	}
	return &Manager{
		store:       s,
		crypto:      c,
		cfg:         cfg,
		log:         log,
		clients:     make(map[string]*tenantClient),
		idleTimeout: idle,
		evictEvery:  every,
		stopCh:      make(chan struct{}),
		stopped:     make(chan struct{}),
	}
}

// StartIdleEviction runs a background reaper until Stop or ctx is done.
// Safe to call once; no-op if idle timeout is disabled (0).
func (m *Manager) StartIdleEviction(ctx context.Context) {
	if m.idleTimeout <= 0 {
		m.log.Info("backend client idle eviction disabled")
		close(m.stopped)
		return
	}
	if m.evictEvery <= 0 {
		m.evictEvery = time.Minute
	}
	m.log.Info("backend client idle eviction enabled",
		"idle_timeout", m.idleTimeout.String(),
		"interval", m.evictEvery.String(),
	)
	go func() {
		defer close(m.stopped)
		t := time.NewTicker(m.evictEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-t.C:
				m.evictIdle(context.Background())
			}
		}
	}()
}

// Stop signals the eviction loop to exit (DisconnectAll still required on shutdown).
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	select {
	case <-m.stopped:
	case <-time.After(5 * time.Second):
	}
}

// Get returns a pooled backend client for a source connection and increments the in-use refcount.
// Caller MUST call Release(connectionID) when the request no longer needs the client
// (typically defer right after a successful Get).
func (m *Manager) Get(ctx context.Context, connectionID string) (*mongo.Client, error) {
	if connectionID == "" {
		return nil, fmt.Errorf("connection id required")
	}
	m.mu.Lock()
	if e, ok := m.clients[connectionID]; ok && e != nil && e.client != nil {
		e.refs++
		e.lastUsed = time.Now()
		c := e.client
		m.mu.Unlock()
		return c, nil
	}
	m.mu.Unlock()

	v, err, _ := m.sf.Do(connectionID, func() (any, error) {
		m.mu.Lock()
		if e, ok := m.clients[connectionID]; ok && e != nil && e.client != nil {
			e.refs++
			e.lastUsed = time.Now()
			c := e.client
			m.mu.Unlock()
			return c, nil
		}
		m.mu.Unlock()

		client, err := m.connect(ctx, connectionID)
		if err != nil {
			return nil, err
		}

		m.mu.Lock()
		if existing, ok := m.clients[connectionID]; ok && existing != nil && existing.client != nil {
			// Lost race: another creator finished; drop ours and use existing.
			existing.refs++
			existing.lastUsed = time.Now()
			c := existing.client
			m.mu.Unlock()
			_ = client.Disconnect(context.Background())
			return c, nil
		}
		m.clients[connectionID] = &tenantClient{
			client:   client,
			lastUsed: time.Now(),
			refs:     1,
		}
		m.mu.Unlock()
		m.log.Info("backend client created", "connection", connectionID)
		return client, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*mongo.Client), nil
}

// Release decrements the in-use refcount for a connection client obtained via Get.
// Safe to call with unknown id (no-op). Idle eviction only runs when refs == 0.
func (m *Manager) Release(connectionID string) {
	if connectionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.clients[connectionID]
	if !ok || e == nil {
		return
	}
	if e.refs > 0 {
		e.refs--
	}
	e.lastUsed = time.Now()
}

func (m *Manager) connect(ctx context.Context, connectionID string) (*mongo.Client, error) {
	conn, err := m.store.GetConnection(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("connection lookup: %w", err)
	}

	// AAD is tenant_id (same as control-plane encryption).
	plaintext, err := m.crypto.Decrypt(conn.URICiphertext, conn.Nonce, conn.TenantID)
	if err != nil {
		return nil, fmt.Errorf("decrypt connection uri: %w", err)
	}
	uri := string(plaintext)

	opts := options.Client().ApplyURI(uri)
	if m.cfg != nil {
		if m.cfg.BackendMaxPoolSize > 0 {
			opts.SetMaxPoolSize(m.cfg.BackendMaxPoolSize)
		}
		if m.cfg.BackendMinPoolSize > 0 {
			opts.SetMinPoolSize(m.cfg.BackendMinPoolSize)
		}
		if m.cfg.BackendConnectTimeout > 0 {
			opts.SetConnectTimeout(m.cfg.BackendConnectTimeout)
			opts.SetServerSelectionTimeout(m.cfg.BackendConnectTimeout)
		}
	}

	connectCtx := ctx
	var cancel context.CancelFunc
	if m.cfg != nil && m.cfg.BackendConnectTimeout > 0 {
		connectCtx, cancel = context.WithTimeout(ctx, m.cfg.BackendConnectTimeout)
		defer cancel()
	}

	client, err := mongo.Connect(connectCtx, opts)
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("backend ping: %w", err)
	}

	return client, nil
}

// evictIdle disconnects tenant clients with refs==0 and lastUsed older than idleTimeout.
func (m *Manager) evictIdle(ctx context.Context) {
	if m.idleTimeout <= 0 {
		return
	}
	now := time.Now()
	type doomed struct {
		id     string
		client *mongo.Client
	}
	var toClose []doomed

	m.mu.Lock()
	for id, e := range m.clients {
		if e == nil {
			delete(m.clients, id)
			continue
		}
		if e.refs > 0 {
			continue
		}
		if now.Sub(e.lastUsed) < m.idleTimeout {
			continue
		}
		toClose = append(toClose, doomed{id: id, client: e.client})
		delete(m.clients, id)
	}
	m.mu.Unlock()

	for _, d := range toClose {
		if d.client == nil {
			m.log.Info("evicted idle backend client", "tenant", d.id, "idle_timeout", m.idleTimeout.String())
			continue
		}
		discCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := d.client.Disconnect(discCtx); err != nil {
			m.log.Warn("idle backend disconnect error", "tenant", d.id, "error", err)
		} else {
			m.log.Info("evicted idle backend client", "tenant", d.id, "idle_timeout", m.idleTimeout.String())
		}
		cancel()
	}
}

// EvictIdleForTest runs one eviction pass (unit tests).
func (m *Manager) EvictIdleForTest(ctx context.Context) {
	m.evictIdle(ctx)
}

// SetIdleTimeoutForTest overrides idle timeout (tests).
func (m *Manager) SetIdleTimeoutForTest(d time.Duration) {
	m.idleTimeout = d
}

// DisconnectAll closes every tenant client. Safe for shutdown.
func (m *Manager) DisconnectAll(ctx context.Context) {
	m.Stop()
	m.mu.Lock()
	clients := m.clients
	m.clients = make(map[string]*tenantClient)
	m.mu.Unlock()

	for id, e := range clients {
		if e == nil || e.client == nil {
			continue
		}
		if err := e.client.Disconnect(ctx); err != nil {
			m.log.Warn("backend disconnect error", "tenant", id, "error", err)
		}
	}
}

// ClientCount returns how many tenant backend clients are currently open (for metrics/tests).
func (m *Manager) ClientCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.clients)
}

// RefsForTest returns in-use count for a tenant (tests).
func (m *Manager) RefsForTest(tenantID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.clients[tenantID]; e != nil {
		return e.refs
	}
	return -1
}

// InjectClientForTest inserts a fake entry without connecting (tests).
func (m *Manager) InjectClientForTest(tenantID string, client *mongo.Client, lastUsed time.Time, refs int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[tenantID] = &tenantClient{client: client, lastUsed: lastUsed, refs: refs}
}
