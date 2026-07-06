package model

import "time"

// Tenant represents a customer / project that uses the accelerator (an "organization" in the UI).
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// User is a dashboard identity authenticated via email OTP.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MemberRole is the role of a user within an organization (tenant).
type MemberRole string

const (
	RoleOwner  MemberRole = "owner"
	RoleAdmin  MemberRole = "admin"
	RoleMember MemberRole = "member"
)

// OrganizationMember links a user to a tenant with a role.
type OrganizationMember struct {
	TenantID  string     `json:"tenantId"`
	UserID    string     `json:"userId"`
	Email     string     `json:"email,omitempty"`
	Name      string     `json:"name,omitempty"`
	Role      MemberRole `json:"role"`
	CreatedAt time.Time  `json:"created_at"`
}

// OrganizationInvite is a pending invite to join a tenant.
type OrganizationInvite struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenantId"`
	TenantName string     `json:"tenantName,omitempty"`
	Email      string     `json:"email"`
	Role       MemberRole `json:"role"`
	InvitedBy  string     `json:"invitedBy,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// RawToken is only set when creating an invite (returned once for email links).
	RawToken string `json:"rawToken,omitempty"`
}

// OrganizationSummary is a tenant the user belongs to (list view).
type OrganizationSummary struct {
	Tenant
	Role MemberRole `json:"role"`
}

// CollectionPolicy configures caching for a specific db.collection.
type CollectionPolicy struct {
	Enabled        bool `json:"enabled"`
	TTLSeconds     int  `json:"ttlSeconds"`
	MaxResultBytes *int `json:"maxResultBytes,omitempty"`
}

// CachePolicy is the declarative caching configuration for a tenant.
type CachePolicy struct {
	TenantID          string                      `json:"tenantId"`
	DefaultTtlSeconds int                         `json:"defaultTtlSeconds"`
	Collections       map[string]CollectionPolicy `json:"collections"`
	CacheKeyVersion   int                         `json:"cacheKeyVersion"`
	UpdatedAt         time.Time                   `json:"updatedAt"`
}

// Token represents proxy access for one source connection (username=tenantId, password=raw secret).
// Clients use the proxy connection URI returned once at issuance.
type Token struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenantId"`
	ConnectionID string     `json:"connectionId,omitempty"`
	Description  string     `json:"description,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// Connection is a named source MongoDB URI for an organization (ciphertext never JSON-exported).
type Connection struct {
	ID            string `json:"id"`
	TenantID      string `json:"tenantId"`
	Name          string `json:"name"`
	URICiphertext []byte `json:"-"`
	Nonce         []byte `json:"-"`
	DEKVersion    string `json:"-"`
	// AutoInvalidateOnWrite flushes proxy cache for a collection after successful writes to it.
	// Default false — TTL + manual invalidate only.
	AutoInvalidateOnWrite bool       `json:"autoInvalidateOnWrite"`
	LastValidatedAt       *time.Time `json:"lastValidatedAt,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}
