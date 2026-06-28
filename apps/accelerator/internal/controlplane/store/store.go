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
	ID        string
	TokenHash string
}

// Store is the interface for control plane persistence.
type Store interface {
	// Tenants
	CreateTenant(ctx context.Context, t *model.Tenant) error
	GetTenant(ctx context.Context, id string) (*model.Tenant, error)
	ListTenants(ctx context.Context) ([]*model.Tenant, error)
	// DeleteTenant removes the tenant row; child tables cascade via FK.
	DeleteTenant(ctx context.Context, id string) error

	// Backends (encrypted)
	SetBackend(ctx context.Context, tenantID string, ciphertext, nonce []byte, dekVersion string) error
	GetBackend(ctx context.Context, tenantID string) (*model.TenantBackend, error)
	UpdateBackendValidated(ctx context.Context, tenantID string) error

	// Policies
	GetCachePolicy(ctx context.Context, tenantID string) (*model.CachePolicy, error)
	UpsertCachePolicy(ctx context.Context, p *model.CachePolicy) error

	// Tokens
	CreateToken(ctx context.Context, tok *model.Token, tokenHash string) error
	GetTokenByID(ctx context.Context, id string) (*model.Token, error)
	ListTokensForTenant(ctx context.Context, tenantID string) ([]*model.Token, error)
	// ListActiveTokenHashes returns id+hash for non-revoked, non-expired tokens of a tenant (proxy auth).
	ListActiveTokenHashes(ctx context.Context, tenantID string) ([]TokenHashRow, error)
	RevokeToken(ctx context.Context, id string) error

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

// ===== Backends =====

func (s *PostgresStore) SetBackend(ctx context.Context, tenantID string, ciphertext, nonce []byte, dekVersion string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tenant_backends (tenant_id, uri_ciphertext, nonce, dek_version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			uri_ciphertext = EXCLUDED.uri_ciphertext,
			nonce = EXCLUDED.nonce,
			dek_version = EXCLUDED.dek_version,
			updated_at = NOW()
	`, tenantID, ciphertext, nonce, dekVersion)
	return err
}

func (s *PostgresStore) GetBackend(ctx context.Context, tenantID string) (*model.TenantBackend, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, uri_ciphertext, nonce, dek_version, last_validated_at, created_at, updated_at
		FROM tenant_backends WHERE tenant_id = $1
	`, tenantID)

	var b model.TenantBackend
	var lastValidated sql.NullTime
	if err := row.Scan(&b.TenantID, &b.URICiphertext, &b.Nonce, &b.DEKVersion, &lastValidated, &b.CreatedAt, &b.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lastValidated.Valid {
		b.LastValidatedAt = &lastValidated.Time
	}
	return &b, nil
}

func (s *PostgresStore) UpdateBackendValidated(ctx context.Context, tenantID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tenant_backends SET last_validated_at = NOW(), updated_at = NOW() WHERE tenant_id = $1
	`, tenantID)
	return err
}

// ===== Cache Policies =====

func (s *PostgresStore) GetCachePolicy(ctx context.Context, tenantID string) (*model.CachePolicy, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, default_ttl_seconds, collections, cache_key_version, updated_at
		FROM cache_policies WHERE tenant_id = $1
	`, tenantID)

	var p model.CachePolicy
	var collectionsJSON []byte
	if err := row.Scan(&p.TenantID, &p.DefaultTtlSeconds, &collectionsJSON, &p.CacheKeyVersion, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Default 60s TTL for all _cache collections; override via policy API.
			return &model.CachePolicy{
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
	collectionsJSON, err := json.Marshal(p.Collections)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO cache_policies (tenant_id, default_ttl_seconds, collections, cache_key_version, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			default_ttl_seconds = EXCLUDED.default_ttl_seconds,
			collections = EXCLUDED.collections,
			cache_key_version = EXCLUDED.cache_key_version,
			updated_at = NOW()
	`, p.TenantID, p.DefaultTtlSeconds, collectionsJSON, p.CacheKeyVersion)
	return err
}

// ===== Tokens =====

func (s *PostgresStore) CreateToken(ctx context.Context, tok *model.Token, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tokens (id, tenant_id, token_hash, description, created_at, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, tok.ID, tok.TenantID, tokenHash, tok.Description, tok.CreatedAt, tok.ExpiresAt, tok.RevokedAt)
	return err
}

func (s *PostgresStore) GetTokenByID(ctx context.Context, id string) (*model.Token, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, description, created_at, expires_at, revoked_at
		FROM tokens WHERE id = $1
	`, id)

	var t model.Token
	var expires, revoked sql.NullTime
	if err := row.Scan(&t.ID, &t.TenantID, &t.Description, &t.CreatedAt, &expires, &revoked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if expires.Valid {
		t.ExpiresAt = &expires.Time
	}
	if revoked.Valid {
		t.RevokedAt = &revoked.Time
	}
	return &t, nil
}

func (s *PostgresStore) ListTokensForTenant(ctx context.Context, tenantID string) ([]*model.Token, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, description, created_at, expires_at, revoked_at
		FROM tokens WHERE tenant_id = $1 ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil empty slice so JSON encodes as [] not null.
	out := make([]*model.Token, 0)
	for rows.Next() {
		var t model.Token
		var expires, revoked sql.NullTime
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Description, &t.CreatedAt, &expires, &revoked); err != nil {
			return nil, err
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

func (s *PostgresStore) ListActiveTokenHashes(ctx context.Context, tenantID string) ([]TokenHashRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, token_hash
		FROM tokens
		WHERE tenant_id = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > NOW())
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TokenHashRow
	for rows.Next() {
		var r TokenHashRow
		if err := rows.Scan(&r.ID, &r.TokenHash); err != nil {
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
