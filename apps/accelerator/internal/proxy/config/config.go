package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds proxy (data plane) runtime configuration.
type Config struct {
	// ListenAddr is the MongoDB wire protocol listen address (e.g. ":27018").
	ListenAddr string
	// HealthAddr is the HTTP health/metrics sidecar (e.g. ":9090").
	HealthAddr string

	DatabaseURL string
	MasterKey   string // via env; loaded by crypto package

	// MaxConnsPerTenant limits open client TCP connections per tenant (0 = unlimited).
	MaxConnsPerTenant int
	// BackendMaxPoolSize / BackendMinPoolSize configure official driver pools.
	BackendMaxPoolSize uint64
	BackendMinPoolSize uint64
	// BackendConnectTimeout is used when establishing backend mongo.Client.
	BackendConnectTimeout time.Duration
	// CursorIdleTimeout prunes server-side cursor state not touched within this window.
	CursorIdleTimeout time.Duration
	// AllowUnauthenticated permits commands without prior auth (dev only; hello still works).
	AllowUnauthenticated bool
}

func Load() *Config {
	listen := getenv("NANCE_PROXY_LISTEN", ":27018")
	health := getenv("NANCE_PROXY_HEALTH_LISTEN", ":9090")

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://nance:nance@localhost:5432/nance?sslmode=disable"
	}

	maxConns := getenvInt("NANCE_PROXY_MAX_CONNS_PER_TENANT", 200)
	maxPool := uint64(getenvInt("NANCE_PROXY_BACKEND_MAX_POOL", 50))
	minPool := uint64(getenvInt("NANCE_PROXY_BACKEND_MIN_POOL", 0))
	connTimeout := getenvDuration("NANCE_PROXY_BACKEND_CONNECT_TIMEOUT", 10*time.Second)
	cursorIdle := getenvDuration("NANCE_PROXY_CURSOR_IDLE_TIMEOUT", 10*time.Minute)
	allowUnauth := getenvBool("NANCE_PROXY_ALLOW_UNAUTH", false)

	return &Config{
		ListenAddr:            listen,
		HealthAddr:            health,
		DatabaseURL:           dbURL,
		MasterKey:             os.Getenv("NANCE_MASTER_KEY"),
		MaxConnsPerTenant:     maxConns,
		BackendMaxPoolSize:    maxPool,
		BackendMinPoolSize:    minPool,
		BackendConnectTimeout: connTimeout,
		CursorIdleTimeout:     cursorIdle,
		AllowUnauthenticated:  allowUnauth,
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getenvBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getenvDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
