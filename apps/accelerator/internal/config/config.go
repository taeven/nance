package config

import (
	"os"
	"strings"
)

type Config struct {
	Port         string
	DatabaseURL  string
	MasterKey    string // passed through to crypto
	AdminToken   string
	MigrationDir string

	// InviteOnly: self-hosters set NANCE_INVITE_ONLY=true so users may only
	// join organizations via invite. When enabled, normal users cannot create
	// organizations (platform admin token can still create tenants for bootstrap).
	InviteOnly bool
}

func Load() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://nance:nance@localhost:5432/nance?sslmode=disable"
	}

	migrations := os.Getenv("MIGRATIONS_DIR")
	if migrations == "" {
		migrations = "./migrations"
	}

	return &Config{
		Port:         ":" + port,
		DatabaseURL:  dbURL,
		MasterKey:    os.Getenv("NANCE_MASTER_KEY"),
		AdminToken:   os.Getenv("NANCE_ADMIN_TOKEN"),
		MigrationDir: migrations,
		InviteOnly:   envBool("NANCE_INVITE_ONLY", false),
	}
}

func (c *Config) GetDatabaseURL() string {
	return c.DatabaseURL
}

// PlatformPublic is safe to expose to the dashboard (no secrets).
type PlatformPublic struct {
	InviteOnly       bool `json:"inviteOnly"`
	AllowOrgCreation bool `json:"allowOrgCreation"` // false when invite-only for end users
	// AllowOrgCreationByAdmin is always true for NANCE_ADMIN_TOKEN bootstrap.
	AllowAdminBootstrap bool `json:"allowAdminBootstrap"`
}

func (c *Config) PlatformPublic() PlatformPublic {
	inviteOnly := c != nil && c.InviteOnly
	return PlatformPublic{
		InviteOnly:          inviteOnly,
		AllowOrgCreation:    !inviteOnly,
		AllowAdminBootstrap: true,
	}
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
