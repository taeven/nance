package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrAuthFailed      = errors.New("authentication failed")
	ErrTenantInactive  = errors.New("tenant is not active")
	ErrNoBackend       = errors.New("backend not configured")
)

// Validator resolves wire credentials (tenant id + raw token) against Postgres.
type Validator struct {
	store store.Store
}

func NewValidator(s store.Store) *Validator {
	return &Validator{store: s}
}

// TenantContext is attached to a connection after successful auth.
type TenantContext struct {
	TenantID string
	TokenID  string
}

// Authenticate checks username (tenant id) + password (raw API token).
func (v *Validator) Authenticate(ctx context.Context, username, password string) (*TenantContext, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, ErrAuthFailed
	}

	tenant, err := v.store.GetTenant(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrAuthFailed
		}
		return nil, err
	}
	if tenant.Status != "" && tenant.Status != "active" {
		return nil, ErrTenantInactive
	}

	// Ensure backend exists (fail fast; pool will also check).
	if _, err := v.store.GetBackend(ctx, username); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNoBackend
		}
		return nil, err
	}

	rows, err := v.store.ListActiveTokenHashes(ctx, username)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if bcrypt.CompareHashAndPassword([]byte(row.TokenHash), []byte(password)) == nil {
			return &TenantContext{TenantID: username, TokenID: row.ID}, nil
		}
	}
	return nil, ErrAuthFailed
}

// ParsePLAINPayload parses SASL PLAIN message: [authzid]\0authcid\0passwd
// MongoDB clients typically send \0<username>\0<password>.
func ParsePLAINPayload(payload []byte) (username, password string, err error) {
	// Split on null bytes; allow leading empty authzid.
	parts := bytes.Split(payload, []byte{0})
	// Common forms:
	//   ["", "user", "pass"]  — 3 parts with empty authzid
	//   ["user", "pass"]      — 2 parts
	//   ["authzid", "user", "pass"]
	switch len(parts) {
	case 2:
		return string(parts[0]), string(parts[1]), nil
	case 3:
		return string(parts[1]), string(parts[2]), nil
	default:
		// Tolerate trailing empty segment
		if len(parts) == 4 && len(parts[3]) == 0 {
			return string(parts[1]), string(parts[2]), nil
		}
		return "", "", ErrAuthFailed
	}
}

// VerifyTokenHash is a small helper for tests / store implementations.
func VerifyTokenHash(raw, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(raw)) == nil
}

// TokenRow is used internally by store lookup.
type TokenRow struct {
	ID        string
	TenantID  string
	TokenHash string
	ExpiresAt *time.Time
	RevokedAt *time.Time
}
