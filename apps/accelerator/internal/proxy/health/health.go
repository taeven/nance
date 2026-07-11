package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/taeven/nance/accelerator/internal/proxy/cachestats"
	"github.com/taeven/nance/accelerator/internal/proxy/pool"
	"github.com/taeven/nance/accelerator/internal/proxy/savings"
)

// Server is a small HTTP sidecar for /healthz, /readyz, /metrics, /cache-stats, /conn-stats, /savings-stats.
type Server struct {
	Addr       string
	ReadyFn    func(ctx context.Context) error
	CacheStats *cachestats.Tracker // optional; process-local hit/miss per collection
	Pool       *pool.Manager       // optional; backend pool snapshot
	Savings    *savings.Tracker    // optional; process-local savings
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if s.ReadyFn != nil {
			if err := s.ReadyFn(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready", "error": err.Error()})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	})
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/cache-stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.CacheStats == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"collections": []any{}, "note": "cache stats not enabled"})
			return
		}
		tenant := r.URL.Query().Get("tenant")
		if tenant != "" {
			_ = json.NewEncoder(w).Encode(s.CacheStats.SnapshotTenant(tenant))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"collections": s.CacheStats.SnapshotAll(),
			"note":        "per-proxy-process counters; aggregate across pods in Prometheus/Grafana if needed",
		})
	})
	mux.HandleFunc("/conn-stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.Pool == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"backends": []any{}, "note": "pool not enabled"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"backends": s.Pool.Snapshot(),
			"note":     "process-local backend pool; use Prometheus nance_proxy_backend_clients for multi-pod",
		})
	})
	mux.HandleFunc("/savings-stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.Savings == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"tenants": []any{}, "note": "savings not enabled"})
			return
		}
		tenant := r.URL.Query().Get("tenant")
		if tenant != "" {
			_ = json.NewEncoder(w).Encode(s.Savings.Snapshot(tenant))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenants": s.Savings.All(),
			"note":    "process-local; product UI uses control-plane Prometheus queries",
		})
	})
	return mux
}

// ListenAndServe runs until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
