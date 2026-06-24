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

// Manager maintains one mongo.Client per tenant, created lazily.
type Manager struct {
	store  store.Store
	crypto *crypto.Config
	cfg    *proxyconfig.Config
	log    *slog.Logger

	mu      sync.RWMutex
	clients map[string]*mongo.Client
	sf      singleflight.Group
}

func NewManager(s store.Store, c *crypto.Config, cfg *proxyconfig.Config, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:   s,
		crypto:  c,
		cfg:     cfg,
		log:     log,
		clients: make(map[string]*mongo.Client),
	}
}

// Get returns a pooled backend client for the tenant.
func (m *Manager) Get(ctx context.Context, tenantID string) (*mongo.Client, error) {
	m.mu.RLock()
	if c, ok := m.clients[tenantID]; ok {
		m.mu.RUnlock()
		return c, nil
	}
	m.mu.RUnlock()

	v, err, _ := m.sf.Do(tenantID, func() (any, error) {
		// Double-check under lock after winning singleflight.
		m.mu.RLock()
		if c, ok := m.clients[tenantID]; ok {
			m.mu.RUnlock()
			return c, nil
		}
		m.mu.RUnlock()

		client, err := m.connect(ctx, tenantID)
		if err != nil {
			return nil, err
		}

		m.mu.Lock()
		// Another goroutine may have inserted; prefer existing.
		if existing, ok := m.clients[tenantID]; ok {
			m.mu.Unlock()
			_ = client.Disconnect(context.Background())
			return existing, nil
		}
		m.clients[tenantID] = client
		m.mu.Unlock()
		m.log.Info("backend client created", "tenant", tenantID)
		return client, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*mongo.Client), nil
}

func (m *Manager) connect(ctx context.Context, tenantID string) (*mongo.Client, error) {
	be, err := m.store.GetBackend(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("backend lookup: %w", err)
	}

	plaintext, err := m.crypto.Decrypt(be.URICiphertext, be.Nonce, tenantID)
	if err != nil {
		return nil, fmt.Errorf("decrypt backend uri: %w", err)
	}
	uri := string(plaintext)
	// Do not retain plaintext beyond Connect options; local var is fine.

	opts := options.Client().ApplyURI(uri)
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

	connectCtx := ctx
	var cancel context.CancelFunc
	if m.cfg.BackendConnectTimeout > 0 {
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

// DisconnectAll closes every tenant client. Safe for shutdown.
func (m *Manager) DisconnectAll(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.clients {
		if err := c.Disconnect(ctx); err != nil {
			m.log.Warn("backend disconnect error", "tenant", id, "error", err)
		}
		delete(m.clients, id)
	}
}

// ClientCount returns how many tenant backend clients are currently open (for metrics/tests).
func (m *Manager) ClientCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients)
}
