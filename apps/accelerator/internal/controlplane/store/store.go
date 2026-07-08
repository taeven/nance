package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/taeven/nance/accelerator/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
)

// TokenHashRow is used by the data-plane proxy to validate raw tokens via bcrypt.
type TokenHashRow struct {
	ID           string
	TokenHash    string
	ConnectionID string
}

// Store is the interface for control plane persistence.
type Store interface {
	// Tenants
	CreateTenant(ctx context.Context, t *model.Tenant) error
	GetTenant(ctx context.Context, id string) (*model.Tenant, error)
	ListTenants(ctx context.Context) ([]*model.Tenant, error)
	// DeleteTenant removes the tenant row; child tables cascade via FK.
	DeleteTenant(ctx context.Context, id string) error

	// Connections (encrypted source Mongo URIs; many per tenant)
	CreateConnection(ctx context.Context, c *model.Connection) error
	UpdateConnectionURI(ctx context.Context, connectionID string, ciphertext, nonce []byte, dekVersion string) error
	UpdateConnectionName(ctx context.Context, connectionID, name string) error
	UpdateConnectionAutoInvalidate(ctx context.Context, connectionID string, enabled bool) error
	GetConnection(ctx context.Context, connectionID string) (*model.Connection, error)
	ListConnections(ctx context.Context, tenantID string) ([]*model.Connection, error)
	DeleteConnection(ctx context.Context, connectionID string) error
	UpdateConnectionValidated(ctx context.Context, connectionID string) error

	// Policies (per connection)
	GetCachePolicy(ctx context.Context, connectionID string) (*model.CachePolicy, error)
	UpsertCachePolicy(ctx context.Context, p *model.CachePolicy) error
	// ListAllCachePolicies returns policies for all connections (proxy refresh).
	ListAllCachePolicies(ctx context.Context) ([]*model.CachePolicy, error)

	// Tokens (bound to a connection)
	CreateToken(ctx context.Context, tok *model.Token, tokenHash string) error
	GetTokenByID(ctx context.Context, id string) (*model.Token, error)
	ListTokensForTenant(ctx context.Context, tenantID string) ([]*model.Token, error)
	ListTokensForConnection(ctx context.Context, connectionID string) ([]*model.Token, error)
	// ListActiveTokenHashes returns id+hash+connection for non-revoked, non-expired tokens of a tenant (proxy auth).
	ListActiveTokenHashes(ctx context.Context, tenantID string) ([]TokenHashRow, error)
	RevokeToken(ctx context.Context, id string) error
	// ClearTokenRevocation clears revoked_at so the token is active again (re-enable within grace window).
	ClearTokenRevocation(ctx context.Context, id string) error

	// Users / sessions / email OTP
	UpsertUserByEmail(ctx context.Context, email, name string) (*model.User, error)
	GetUserByID(ctx context.Context, id string) (*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	UpdateUserName(ctx context.Context, id, name string) error
	SetEmailVerificationCode(ctx context.Context, email, codeHash string, expiresAt time.Time) error
	GetEmailVerificationCode(ctx context.Context, email string) (codeHash string, expiresAt time.Time, attempts int, err error)
	IncrementEmailVerificationAttempts(ctx context.Context, email string) error
	ClearEmailVerificationCode(ctx context.Context, email string) error
	CreateSession(ctx context.Context, id, userID, tokenHash string, expiresAt time.Time) error
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (sessionID, userID string, expiresAt time.Time, err error)
	DeleteSession(ctx context.Context, sessionID string) error
	DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error

	// Organization membership / invites
	AddMember(ctx context.Context, tenantID, userID string, role model.MemberRole) error
	RemoveMember(ctx context.Context, tenantID, userID string) error
	GetMember(ctx context.Context, tenantID, userID string) (*model.OrganizationMember, error)
	ListMembers(ctx context.Context, tenantID string) ([]*model.OrganizationMember, error)
	ListOrganizationsForUser(ctx context.Context, userID string) ([]*model.OrganizationSummary, error)
	CreateInvite(ctx context.Context, inv *model.OrganizationInvite, tokenHash string) error
	GetInviteByID(ctx context.Context, id string) (*model.OrganizationInvite, error)
	GetPendingInviteByTokenHash(ctx context.Context, tokenHash string) (*model.OrganizationInvite, error)
	ListPendingInvitesForEmail(ctx context.Context, email string) ([]*model.OrganizationInvite, error)
	ListPendingInvitesForTenant(ctx context.Context, tenantID string) ([]*model.OrganizationInvite, error)
	MarkInviteAccepted(ctx context.Context, id string) error
	DeleteInvite(ctx context.Context, id string) error

	// Audit (best effort)
	RecordAudit(ctx context.Context, tenantID, actor, action string, payload any) error

	Close() error
}

// PostgresStore implements Store using pgxpool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

// RunMigrations applies all up migrations in lexical order from the provided directory.
// For Phase 0 we keep it simple: caller passes the migration dir and we execute *.up.sql files in order.
func (s *PostgresStore) RunMigrations(ctx context.Context, migrationDir string) error {
	// In this implementation we expect the caller (main) to have already run migrations
	// via a small embedded or file-based runner before creating the store, or we can
	// implement a tiny runner here. For cleanliness we provide the method but main will drive it.
	// See cmd/controlplane for the actual simple migration runner.
	return nil
}

// ===== Tenants =====

func (s *PostgresStore) CreateTenant(ctx context.Context, t *model.Tenant) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tenants (id, name, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING
	`, t.ID, t.Name, t.Status, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *PostgresStore) GetTenant(ctx context.Context, id string) (*model.Tenant, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, status, created_at, updated_at
		FROM tenants WHERE id = $1
	`, id)

	var t model.Tenant
	if err := row.Scan(&t.ID, &t.Name, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

func (s *PostgresStore) ListTenants(ctx context.Context) ([]*model.Tenant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, status, created_at, updated_at
		FROM tenants ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil empty slice so JSON encodes as [] not null.
	out := make([]*model.Tenant, 0)
	for rows.Next() {
		var t model.Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteTenant(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ===== Connections =====

func (s *PostgresStore) CreateConnection(ctx context.Context, c *model.Connection) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO connections (id, tenant_id, name, uri_ciphertext, nonce, dek_version, auto_invalidate_on_write, last_validated_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, c.ID, c.TenantID, c.Name, c.URICiphertext, c.Nonce, c.DEKVersion, c.AutoInvalidateOnWrite, c.LastValidatedAt, c.CreatedAt, c.UpdatedAt)
	return err
}

func (s *PostgresStore) UpdateConnectionURI(ctx context.Context, connectionID string, ciphertext, nonce []byte, dekVersion string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE connections SET uri_ciphertext = $2, nonce = $3, dek_version = $4, updated_at = NOW()
		WHERE id = $1
	`, connectionID, ciphertext, nonce, dekVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateConnectionName(ctx context.Context, connectionID, name string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE connections SET name = $2, updated_at = NOW() WHERE id = $1
	`, connectionID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateConnectionAutoInvalidate(ctx context.Context, connectionID string, enabled bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE connections SET auto_invalidate_on_write = $2, updated_at = NOW() WHERE id = $1
	`, connectionID, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanConnection(row pgx.Row) (*model.Connection, error) {
	var c model.Connection
	var lastValidated sql.NullTime
	if err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.URICiphertext, &c.Nonce, &c.DEKVersion, &c.AutoInvalidateOnWrite, &lastValidated, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lastValidated.Valid {
		c.LastValidatedAt = &lastValidated.Time
	}
	return &c, nil
}

const connectionSelectCols = `id, tenant_id, name, uri_ciphertext, nonce, dek_version, auto_invalidate_on_write, last_validated_at, created_at, updated_at`

func (s *PostgresStore) GetConnection(ctx context.Context, connectionID string) (*model.Connection, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+connectionSelectCols+`
		FROM connections WHERE id = $1
	`, connectionID)
	return scanConnection(row)
}

func (s *PostgresStore) ListConnections(ctx context.Context, tenantID string) ([]*model.Connection, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+connectionSelectCols+`
		FROM connections WHERE tenant_id = $1 ORDER BY created_at ASC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*model.Connection, 0)
	for rows.Next() {
		var c model.Connection
		var lastValidated sql.NullTime
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.URICiphertext, &c.Nonce, &c.DEKVersion, &c.AutoInvalidateOnWrite, &lastValidated, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if lastValidated.Valid {
			c.LastValidatedAt = &lastValidated.Time
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteConnection(ctx context.Context, connectionID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM connections WHERE id = $1`, connectionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateConnectionValidated(ctx context.Context, connectionID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE connections SET last_validated_at = NOW(), updated_at = NOW() WHERE id = $1
	`, connectionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ===== Cache Policies (per connection) =====

func (s *PostgresStore) GetCachePolicy(ctx context.Context, connectionID string) (*model.CachePolicy, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT connection_id, tenant_id, default_ttl_seconds, collections, cache_key_version, updated_at
		FROM connection_cache_policies WHERE connection_id = $1
	`, connectionID)

	var p model.CachePolicy
	var collectionsJSON []byte
	if err := row.Scan(&p.ConnectionID, &p.TenantID, &p.DefaultTtlSeconds, &collectionsJSON, &p.CacheKeyVersion, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Resolve tenant from connection when possible for a coherent default.
			tenantID := ""
			if c, cerr := s.GetConnection(ctx, connectionID); cerr == nil && c != nil {
				tenantID = c.TenantID
			}
			return &model.CachePolicy{
				ConnectionID:      connectionID,
				TenantID:          tenantID,
				DefaultTtlSeconds: 60,
				Collections:       map[string]model.CollectionPolicy{},
				CacheKeyVersion:   1,
			}, nil
		}
		return nil, err
	}

	if len(collectionsJSON) > 0 {
		if err := json.Unmarshal(collectionsJSON, &p.Collections); err != nil {
			return nil, fmt.Errorf("failed to unmarshal collections: %w", err)
		}
	} else {
		p.Collections = map[string]model.CollectionPolicy{}
	}
	return &p, nil
}

func (s *PostgresStore) UpsertCachePolicy(ctx context.Context, p *model.CachePolicy) error {
	if p.ConnectionID == "" {
		return fmt.Errorf("connectionId is required")
	}
	if p.TenantID == "" {
		c, err := s.GetConnection(ctx, p.ConnectionID)
		if err != nil {
			return err
		}
		p.TenantID = c.TenantID
	}
	collectionsJSON, err := json.Marshal(p.Collections)
	if err != nil {
		return err
	}
	if p.Collections == nil {
		collectionsJSON = []byte("{}")
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO connection_cache_policies (connection_id, tenant_id, default_ttl_seconds, collections, cache_key_version, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (connection_id) DO UPDATE SET
			default_ttl_seconds = EXCLUDED.default_ttl_seconds,
			collections = EXCLUDED.collections,
			cache_key_version = EXCLUDED.cache_key_version,
			updated_at = NOW()
	`, p.ConnectionID, p.TenantID, p.DefaultTtlSeconds, collectionsJSON, p.CacheKeyVersion)
	return err
}

func (s *PostgresStore) ListAllCachePolicies(ctx context.Context) ([]*model.CachePolicy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT connection_id, tenant_id, default_ttl_seconds, collections, cache_key_version, updated_at
		FROM connection_cache_policies
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*model.CachePolicy, 0)
	for rows.Next() {
		var p model.CachePolicy
		var collectionsJSON []byte
		if err := rows.Scan(&p.ConnectionID, &p.TenantID, &p.DefaultTtlSeconds, &collectionsJSON, &p.CacheKeyVersion, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if len(collectionsJSON) > 0 {
			if err := json.Unmarshal(collectionsJSON, &p.Collections); err != nil {
				return nil, err
			}
		} else {
			p.Collections = map[string]model.CollectionPolicy{}
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ===== Tokens =====

func (s *PostgresStore) CreateToken(ctx context.Context, tok *model.Token, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tokens (id, tenant_id, connection_id, token_hash, description, created_at, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, tok.ID, tok.TenantID, nullIfEmpty(tok.ConnectionID), tokenHash, tok.Description, tok.CreatedAt, tok.ExpiresAt, tok.RevokedAt)
	return err
}

func scanToken(row interface {
	Scan(dest ...any) error
}) (*model.Token, error) {
	var t model.Token
	var connID sql.NullString
	var expires, revoked sql.NullTime
	if err := row.Scan(&t.ID, &t.TenantID, &connID, &t.Description, &t.CreatedAt, &expires, &revoked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if connID.Valid {
		t.ConnectionID = connID.String
	}
	if expires.Valid {
		t.ExpiresAt = &expires.Time
	}
	if revoked.Valid {
		t.RevokedAt = &revoked.Time
	}
	return &t, nil
}

func (s *PostgresStore) GetTokenByID(ctx context.Context, id string) (*model.Token, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, connection_id, description, created_at, expires_at, revoked_at
		FROM tokens WHERE id = $1
	`, id)
	return scanToken(row)
}

func (s *PostgresStore) listTokens(ctx context.Context, query string, arg string) ([]*model.Token, error) {
	rows, err := s.pool.Query(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*model.Token, 0)
	for rows.Next() {
		var t model.Token
		var connID sql.NullString
		var expires, revoked sql.NullTime
		if err := rows.Scan(&t.ID, &t.TenantID, &connID, &t.Description, &t.CreatedAt, &expires, &revoked); err != nil {
			return nil, err
		}
		if connID.Valid {
			t.ConnectionID = connID.String
		}
		if expires.Valid {
			t.ExpiresAt = &expires.Time
		}
		if revoked.Valid {
			t.RevokedAt = &revoked.Time
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListTokensForTenant(ctx context.Context, tenantID string) ([]*model.Token, error) {
	return s.listTokens(ctx, `
		SELECT id, tenant_id, connection_id, description, created_at, expires_at, revoked_at
		FROM tokens WHERE tenant_id = $1 ORDER BY created_at DESC
	`, tenantID)
}

func (s *PostgresStore) ListTokensForConnection(ctx context.Context, connectionID string) ([]*model.Token, error) {
	return s.listTokens(ctx, `
		SELECT id, tenant_id, connection_id, description, created_at, expires_at, revoked_at
		FROM tokens WHERE connection_id = $1 ORDER BY created_at DESC
	`, connectionID)
}

func (s *PostgresStore) ListActiveTokenHashes(ctx context.Context, tenantID string) ([]TokenHashRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, token_hash, COALESCE(connection_id, '')
		FROM tokens
		WHERE tenant_id = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > NOW())
		  AND connection_id IS NOT NULL
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TokenHashRow
	for rows.Next() {
		var r TokenHashRow
		if err := rows.Scan(&r.ID, &r.TokenHash, &r.ConnectionID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RevokeToken(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tokens SET revoked_at = NOW() WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) ClearTokenRevocation(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tokens SET revoked_at = NULL WHERE id = $1 AND revoked_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ===== Audit =====

func (s *PostgresStore) RecordAudit(ctx context.Context, tenantID, actor, action string, payload any) error {
	var payloadJSON []byte
	if payload != nil {
		b, _ := json.Marshal(payload) // best effort
		payloadJSON = b
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_logs (tenant_id, actor, action, payload)
		VALUES ($1, $2, $3, $4)
	`, tenantID, actor, action, payloadJSON)
	// Audit is best-effort; don't fail main operations
	if err != nil {
		// In real life we would log this
		_ = err
	}
	return nil
}
