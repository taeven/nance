package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/crypto"
	"github.com/taeven/nance/accelerator/internal/model"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrTenantNotFound     = errors.New("tenant not found")
	ErrConnectionNotFound = errors.New("connection not found")
	ErrDuplicateName      = errors.New("connection name already exists")
	ErrInvalidToken       = errors.New("invalid token")
	ErrPolicyNotFound     = errors.New("policy not found")
)

// TenantService handles tenant lifecycle.
type TenantService struct {
	store store.Store
}

func NewTenantService(s store.Store) *TenantService {
	return &TenantService{store: s}
}

func (s *TenantService) Create(ctx context.Context, id, name string) (*model.Tenant, error) {
	if id == "" || name == "" {
		return nil, errors.New("id and name are required")
	}
	t := &model.Tenant{
		ID:        id,
		Name:      name,
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateTenant(ctx, t); err != nil {
		return nil, err
	}
	_ = s.store.RecordAudit(ctx, id, "system", "create_tenant", map[string]string{"name": name})
	return t, nil
}

func (s *TenantService) Get(ctx context.Context, id string) (*model.Tenant, error) {
	t, err := s.store.GetTenant(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, err
	}
	return t, nil
}

func (s *TenantService) List(ctx context.Context) ([]*model.Tenant, error) {
	return s.store.ListTenants(ctx)
}

// ConnectionService manages named encrypted source Mongo URIs per organization.
type ConnectionService struct {
	store  store.Store
	crypto *crypto.Config
}

// NewConnectionService creates a multi-connection service (replaces single-backend API).
func NewConnectionService(s store.Store, c *crypto.Config) *ConnectionService {
	return &ConnectionService{store: s, crypto: c}
}

// NewBackendService is an alias for NewConnectionService (legacy name used by wiring).
func NewBackendService(s store.Store, c *crypto.Config) *ConnectionService {
	return NewConnectionService(s, c)
}

func publicConnection(c *model.Connection) *model.Connection {
	if c == nil {
		return nil
	}
	return &model.Connection{
		ID:              c.ID,
		TenantID:        c.TenantID,
		Name:            c.Name,
		LastValidatedAt: c.LastValidatedAt,
		CreatedAt:       c.CreatedAt,
		UpdatedAt:       c.UpdatedAt,
	}
}

// Create encrypts and stores a new named source connection for the tenant.
func (s *ConnectionService) Create(ctx context.Context, tenantID, name, plaintextURI string) (*model.Connection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if plaintextURI == "" {
		return nil, errors.New("uri is required")
	}
	if _, err := s.store.GetTenant(ctx, tenantID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrTenantNotFound
		}
		return nil, err
	}
	ct, nonce, dekVer, err := s.crypto.Encrypt([]byte(plaintextURI), tenantID)
	if err != nil {
		return nil, fmt.Errorf("encryption failed: %w", err)
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	c := &model.Connection{
		ID:            "conn_" + base64.RawURLEncoding.EncodeToString(buf),
		TenantID:      tenantID,
		Name:          name,
		URICiphertext: ct,
		Nonce:         nonce,
		DEKVersion:    dekVer,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.store.CreateConnection(ctx, c); err != nil {
		if errors.Is(err, store.ErrDuplicate) || strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			return nil, ErrDuplicateName
		}
		return nil, err
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "create_connection", map[string]string{"connection_id": c.ID, "name": name})
	return publicConnection(c), nil
}

// List returns non-secret metadata for all connections in the tenant.
func (s *ConnectionService) List(ctx context.Context, tenantID string) ([]*model.Connection, error) {
	list, err := s.store.ListConnections(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Connection, 0, len(list))
	for _, c := range list {
		out = append(out, publicConnection(c))
	}
	return out, nil
}

// Get returns non-secret metadata for one connection (must belong to tenant).
func (s *ConnectionService) Get(ctx context.Context, tenantID, connectionID string) (*model.Connection, error) {
	c, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}
	if c.TenantID != tenantID {
		return nil, ErrConnectionNotFound
	}
	return publicConnection(c), nil
}

// UpdateURI replaces the encrypted source URI for a connection.
func (s *ConnectionService) UpdateURI(ctx context.Context, tenantID, connectionID, plaintextURI string) error {
	if plaintextURI == "" {
		return errors.New("uri is required")
	}
	c, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrConnectionNotFound
		}
		return err
	}
	if c.TenantID != tenantID {
		return ErrConnectionNotFound
	}
	ct, nonce, dekVer, err := s.crypto.Encrypt([]byte(plaintextURI), tenantID)
	if err != nil {
		return fmt.Errorf("encryption failed: %w", err)
	}
	if err := s.store.UpdateConnectionURI(ctx, connectionID, ct, nonce, dekVer); err != nil {
		return err
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "update_connection_uri", map[string]string{"connection_id": connectionID})
	return nil
}

// Rename updates the human-readable connection name.
func (s *ConnectionService) Rename(ctx context.Context, tenantID, connectionID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name is required")
	}
	c, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrConnectionNotFound
		}
		return err
	}
	if c.TenantID != tenantID {
		return ErrConnectionNotFound
	}
	if err := s.store.UpdateConnectionName(ctx, connectionID, name); err != nil {
		if errors.Is(err, store.ErrDuplicate) || strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			return ErrDuplicateName
		}
		return err
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "rename_connection", map[string]string{"connection_id": connectionID, "name": name})
	return nil
}

// Delete removes a connection and cascades its tokens.
func (s *ConnectionService) Delete(ctx context.Context, tenantID, connectionID string) error {
	c, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrConnectionNotFound
		}
		return err
	}
	if c.TenantID != tenantID {
		return ErrConnectionNotFound
	}
	if err := s.store.DeleteConnection(ctx, connectionID); err != nil {
		return err
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "delete_connection", map[string]string{"connection_id": connectionID})
	return nil
}

// Test decrypts the URI, pings Mongo, never returns the URI.
func (s *ConnectionService) Test(ctx context.Context, tenantID, connectionID string) error {
	c, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrConnectionNotFound
		}
		return err
	}
	if c.TenantID != tenantID {
		return ErrConnectionNotFound
	}

	plaintext, err := s.crypto.Decrypt(c.URICiphertext, c.Nonce, tenantID)
	if err != nil {
		return fmt.Errorf("failed to decrypt connection uri: %w", err)
	}
	uri := string(plaintext)

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return fmt.Errorf("failed to connect to real MongoDB: %w", err)
	}
	defer client.Disconnect(ctx)

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}

	if _, err := client.ListDatabases(ctx, nil); err != nil {
		return fmt.Errorf("listDatabases failed: %w", err)
	}

	_ = s.store.UpdateConnectionValidated(ctx, connectionID)
	_ = s.store.RecordAudit(ctx, tenantID, "system", "test_connection", map[string]any{"connection_id": connectionID, "success": true})
	return nil
}

// PolicyService manages cache policies.
// CacheInvalidator is implemented by the proxy cache layer (optional on control plane).
type CacheInvalidator interface {
	InvalidateNamespace(ctx context.Context, tenantID, db, coll string) error
	InvalidateTags(ctx context.Context, tenantID string, tags []string) error
}

type PolicyService struct {
	store store.Store
	cache CacheInvalidator // optional; when nil only audit is recorded
}

func NewPolicyService(s store.Store) *PolicyService {
	return &PolicyService{store: s}
}

// WithCache attaches a Redis-backed invalidator for explicit flushes.
func (s *PolicyService) WithCache(c CacheInvalidator) *PolicyService {
	s.cache = c
	return s
}

func (s *PolicyService) Get(ctx context.Context, tenantID string) (*model.CachePolicy, error) {
	p, err := s.store.GetCachePolicy(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PolicyService) SetCollectionPolicy(ctx context.Context, tenantID, dbColl string, pol model.CollectionPolicy) error {
	current, err := s.store.GetCachePolicy(ctx, tenantID)
	if err != nil {
		return err
	}
	if current.Collections == nil {
		current.Collections = make(map[string]model.CollectionPolicy)
	}
	current.Collections[dbColl] = pol
	current.TenantID = tenantID
	if err := s.store.UpsertCachePolicy(ctx, current); err != nil {
		return err
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "update_collection_policy", map[string]string{"collection": dbColl})
	return nil
}

func (s *PolicyService) SetDefaults(ctx context.Context, tenantID string, defaultTTL int) error {
	current, err := s.store.GetCachePolicy(ctx, tenantID)
	if err != nil {
		return err
	}
	current.DefaultTtlSeconds = defaultTTL
	current.TenantID = tenantID
	if err := s.store.UpsertCachePolicy(ctx, current); err != nil {
		return err
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "update_default_ttl", map[string]int{"ttl": defaultTTL})
	return nil
}

// Invalidate flushes cache entries for a namespace and/or tags (Phase 3).
func (s *PolicyService) Invalidate(ctx context.Context, tenantID, db, coll string, tags []string) error {
	if s.cache != nil {
		if db != "" && coll != "" {
			if err := s.cache.InvalidateNamespace(ctx, tenantID, db, coll); err != nil {
				return err
			}
		}
		if len(tags) > 0 {
			if err := s.cache.InvalidateTags(ctx, tenantID, tags); err != nil {
				return err
			}
		}
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "invalidate_cache", map[string]any{
		"db": db, "coll": coll, "tags": tags,
	})
	return nil
}

// TokenService issues and manages data-plane tokens (proxy access for a connection).
type TokenService struct {
	store               store.Store
	proxyPublicEndpoint string
}

func NewTokenService(s store.Store) *TokenService {
	return &TokenService{store: s, proxyPublicEndpoint: "127.0.0.1:27018"}
}

// WithProxyPublicEndpoint sets the host[:port] used in issued proxy connection URIs.
func (s *TokenService) WithProxyPublicEndpoint(endpoint string) *TokenService {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint != "" {
		s.proxyPublicEndpoint = endpoint
	}
	return s
}

// IssuedAccess is returned once when creating proxy access for a connection.
type IssuedAccess struct {
	RawToken           string
	Token              *model.Token
	ProxyConnectionURI string
}

// BuildProxyConnectionURI builds a client URI for the data-plane proxy (PLAIN auth).
// endpoint is host[:port] (scheme optional). No default database path — clients choose the DB.
func BuildProxyConnectionURI(endpoint, tenantID, rawToken string) string {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "mongodb://")
	endpoint = strings.TrimPrefix(endpoint, "mongodb+srv://")
	endpoint = strings.TrimSuffix(endpoint, "/")
	if endpoint == "" {
		endpoint = "127.0.0.1:27018"
	}
	u := &url.URL{
		Scheme: "mongodb",
		User:   url.UserPassword(tenantID, rawToken),
		Host:   endpoint,
		Path:   "/",
	}
	q := url.Values{}
	q.Set("authMechanism", "PLAIN")
	q.Set("authSource", "$external")
	u.RawQuery = q.Encode()
	return u.String()
}

// Issue creates proxy access bound to a source connection.
// Returns the raw secret and full proxy connection URI only once.
func (s *TokenService) Issue(ctx context.Context, tenantID, connectionID, description string) (*IssuedAccess, error) {
	c, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}
	if c.TenantID != tenantID {
		return nil, ErrConnectionNotFound
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	rawToken := base64.RawURLEncoding.EncodeToString(buf)

	hash, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	tok := &model.Token{
		ID:           "tok_" + base64.RawURLEncoding.EncodeToString(buf[:12]),
		TenantID:     tenantID,
		ConnectionID: connectionID,
		Description:  description,
		CreatedAt:    now,
	}

	if err := s.store.CreateToken(ctx, tok, string(hash)); err != nil {
		return nil, err
	}
	_ = s.store.RecordAudit(ctx, tenantID, "system", "issue_token", map[string]string{
		"token_id": tok.ID, "connection_id": connectionID,
	})

	uri := BuildProxyConnectionURI(s.proxyPublicEndpoint, tenantID, rawToken)
	return &IssuedAccess{
		RawToken:           rawToken,
		Token:              tok,
		ProxyConnectionURI: uri,
	}, nil
}

func (s *TokenService) ListForConnection(ctx context.Context, tenantID, connectionID string) ([]*model.Token, error) {
	c, err := s.store.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}
	if c.TenantID != tenantID {
		return nil, ErrConnectionNotFound
	}
	return s.store.ListTokensForConnection(ctx, connectionID)
}

func (s *TokenService) List(ctx context.Context, tenantID string) ([]*model.Token, error) {
	return s.store.ListTokensForTenant(ctx, tenantID)
}

func (s *TokenService) Get(ctx context.Context, tokenID string) (*model.Token, error) {
	tok, err := s.store.GetTokenByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	return tok, nil
}

func (s *TokenService) Revoke(ctx context.Context, tokenID string) error {
	return s.store.RevokeToken(ctx, tokenID)
}
