package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	TenantCreated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nance_tenants_created_total",
		Help: "Number of tenants created",
	})

	BackendTestSuccess = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_backend_test_success_total",
		Help: "Successful backend connection tests",
	}, []string{"tenant"})

	PolicyUpdates = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_policy_updates_total",
		Help: "Cache policy updates",
	}, []string{"tenant", "type"})

	// --- Proxy (Phase 1 data plane) ---

	ProxyConnectionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "nance_proxy_connections_active",
		Help: "Active client TCP connections to the proxy (global, includes unauthenticated)",
	})

	// Product primary: authenticated open TCP sessions per tenant (cluster-sum via Prom).
	ProxyClientConnectionsAuthenticated = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nance_proxy_client_connections_authenticated",
		Help: "Authenticated client TCP connections per tenant",
	}, []string{"tenant"})

	// Ops secondary: in-flight command handling (often ~0 at scrape; sequential per conn).
	ProxyClientConnectionsBusy = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nance_proxy_client_connections_busy",
		Help: "Authenticated client connections currently handling a command",
	}, []string{"tenant"})

	ProxyCommands = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_proxy_commands_total",
		Help: "Commands handled by the proxy",
	}, []string{"tenant", "command"})

	ProxyCommandDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nance_proxy_command_duration_seconds",
		Help:    "Command handling latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"command"})

	ProxyAuthSuccess = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_proxy_auth_success_total",
		Help: "Successful PLAIN authentications",
	}, []string{"tenant"})

	ProxyAuthFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nance_proxy_auth_failures_total",
		Help: "Failed authentication attempts",
	})

	ProxyBackendErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_proxy_backend_errors_total",
		Help: "Errors talking to tenant backend MongoDB",
	}, []string{"tenant"})

	// Backend pool: one series per tenant × state (in_use|idle).
	ProxyBackendClients = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nance_proxy_backend_clients",
		Help: "Backend mongo.Client pool entries per tenant and state (in_use|idle)",
	}, []string{"tenant", "state"})

	ProxyBackendClientsCreated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_proxy_backend_clients_created_total",
		Help: "Backend mongo.Client creations",
	}, []string{"tenant"})

	ProxyBackendClientsEvicted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_proxy_backend_clients_evicted_total",
		Help: "Backend mongo.Client idle evictions",
	}, []string{"tenant"})

	// --- Cache ---

	// Low-cardinality SoT for product hit/miss/bypass (result = hit|miss|bypass).
	CacheRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_cache_requests_total",
		Help: "Cache path outcomes (hit, miss, bypass)",
	}, []string{"tenant", "result"})

	CacheBytesServed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_cache_bytes_served_total",
		Help: "Response bytes attributed to cache or backend path",
	}, []string{"tenant", "source"}) // cache | backend

	// Legacy high-cardinality metrics (dual-write while Grafana migrates). Prefer CacheRequests.
	CacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_cache_hits_total",
		Help: "Cache hits served from Redis (legacy; includes ns label)",
	}, []string{"tenant", "ns", "command"})

	CacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_cache_misses_total",
		Help: "Cache misses that executed against backend (legacy; includes ns label)",
	}, []string{"tenant", "ns", "command"})

	CacheBypass = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_cache_bypass_total",
		Help: "Commands that skipped cache",
	}, []string{"tenant", "reason"})

	CacheInvalidations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_cache_invalidations_total",
		Help: "Namespace invalidations",
	}, []string{"tenant", "ns", "reason"})

	CacheUnavailable = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nance_cache_redis_unavailable_total",
		Help: "Redis errors on hot path (fail-open)",
	})

	// Global size histogram (no tenant label — cardinality budget).
	CacheResultBytes = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "nance_cache_result_bytes",
		Help:    "Size of payloads stored in cache",
		Buckets: []float64{256, 1024, 4096, 16384, 65536, 262144, 1048576},
	})

	CacheLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nance_cache_latency_seconds",
		Help:    "Cache path latency (hit vs miss populate)",
		Buckets: prometheus.DefBuckets,
	}, []string{"path"}) // hit | miss

	// --- Phase 3 hardening ---

	ProxyRateLimited = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_proxy_rate_limited_total",
		Help: "Commands rejected by per-tenant rate limiter",
	}, []string{"tenant"})

	ProxyCachedCursorsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "nance_proxy_cached_cursors_active",
		Help: "In-memory emulated cursors for cache hits",
	})

	CacheExplicitInvalidations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "nance_cache_explicit_invalidations_total",
		Help: "Control-plane / explicit invalidation requests",
	}, []string{"tenant", "kind"}) // ns | tag
)

// SetGaugeTenant sets a gauge and deletes the series when value is 0 (churn hygiene).
func SetGaugeTenant(g *prometheus.GaugeVec, tenant string, value float64) {
	if tenant == "" {
		return
	}
	if value <= 0 {
		_ = g.DeleteLabelValues(tenant)
		return
	}
	g.WithLabelValues(tenant).Set(value)
}

// IncGaugeTenant increments a per-tenant gauge.
func IncGaugeTenant(g *prometheus.GaugeVec, tenant string) {
	if tenant == "" {
		return
	}
	g.WithLabelValues(tenant).Inc()
}

// DecGaugeTenant decrements a per-tenant gauge; deletes series at <=0 via GetMetricWithLabelValues is hard —
// we Dec and leave zero (Prometheus still holds series). Prefer explicit Set when counts are known.
func DecGaugeTenant(g *prometheus.GaugeVec, tenant string) {
	if tenant == "" {
		return
	}
	g.WithLabelValues(tenant).Dec()
}
