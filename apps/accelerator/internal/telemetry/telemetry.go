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
		Help: "Active client TCP connections to the proxy",
	})

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
)
