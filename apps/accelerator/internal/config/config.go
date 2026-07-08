package config

import (
	"os"
	"strings"
	"time"
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

	// ProxyPublicEndpoint is host[:port] used when building client proxy
	// connection URIs (e.g. "127.0.0.1:27018" or "proxy.example.com:27018").
	ProxyPublicEndpoint string

	// TokenReenableWindow is how long after revoke a proxy token may be re-enabled.
	// Default 5m. Set NANCE_TOKEN_REENABLE_WINDOW=0 to disable.
	TokenReenableWindow time.Duration
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

	proxyEndpoint := strings.TrimSpace(os.Getenv("NANCE_PROXY_PUBLIC_ENDPOINT"))
	if proxyEndpoint == "" {
		proxyEndpoint = "127.0.0.1:27018"
	}

	return &Config{
		Port:                ":" + port,
		DatabaseURL:         dbURL,
		MasterKey:           os.Getenv("NANCE_MASTER_KEY"),
		AdminToken:          os.Getenv("NANCE_ADMIN_TOKEN"),
		MigrationDir:        migrations,
		InviteOnly:          envBool("NANCE_INVITE_ONLY", false),
		ProxyPublicEndpoint: proxyEndpoint,
		TokenReenableWindow: envDurationAllowZero("NANCE_TOKEN_REENABLE_WINDOW", 5*time.Minute),
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
	// ProxyPublicEndpoint is host[:port] for building client proxy connection URIs.
	ProxyPublicEndpoint string `json:"proxyPublicEndpoint"`
	// TokenReenableWindowSeconds is the grace period to re-enable a revoked proxy token (0 = disabled).
	TokenReenableWindowSeconds int `json:"tokenReenableWindowSeconds"`
}

func (c *Config) PlatformPublic() PlatformPublic {
	inviteOnly := c != nil && c.InviteOnly
	endpoint := "127.0.0.1:27018"
	window := 5 * time.Minute
	if c != nil {
		if c.ProxyPublicEndpoint != "" {
			endpoint = c.ProxyPublicEndpoint
		}
		window = c.TokenReenableWindow
		if window < 0 {
			window = 0
		}
	}
	return PlatformPublic{
		InviteOnly:                 inviteOnly,
		AllowOrgCreation:           !inviteOnly,
		AllowAdminBootstrap:        true,
		ProxyPublicEndpoint:        endpoint,
		TokenReenableWindowSeconds: int(window / time.Second),
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

// envDurationAllowZero treats missing env as def, but explicit "0" as zero (disable feature).
func envDurationAllowZero(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if v == "0" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	if d < 0 {
		return 0
	}
	return d
}
