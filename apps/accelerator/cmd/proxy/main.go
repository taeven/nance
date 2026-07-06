package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/crypto"
	"github.com/taeven/nance/accelerator/internal/dotenv"
	"github.com/taeven/nance/accelerator/internal/proxy/auth"
	"github.com/taeven/nance/accelerator/internal/proxy/cache"
	"github.com/taeven/nance/accelerator/internal/proxy/cachedcursor"
	"github.com/taeven/nance/accelerator/internal/proxy/cachestats"
	proxyconfig "github.com/taeven/nance/accelerator/internal/proxy/config"
	"github.com/taeven/nance/accelerator/internal/proxy/cursor"
	"github.com/taeven/nance/accelerator/internal/proxy/health"
	"github.com/taeven/nance/accelerator/internal/proxy/policy"
	"github.com/taeven/nance/accelerator/internal/proxy/pool"
	"github.com/taeven/nance/accelerator/internal/proxy/ratelimit"
	"github.com/taeven/nance/accelerator/internal/proxy/region"
	"github.com/taeven/nance/accelerator/internal/proxy/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Optional local .env / .env.local (cwd). Existing process env wins.
	if err := dotenv.Load(); err != nil {
		logger.Warn("dotenv load failed", "error", err)
	}

	cfg := proxyconfig.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Postgres (token + backend lookup; same DB as control plane)
	pgStore, err := store.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer pgStore.Close()

	cryptoCfg, err := crypto.NewConfigFromEnv(os.Getenv)
	if err != nil {
		logger.Error("crypto init failed (set NANCE_MASTER_KEY)", "error", err)
		os.Exit(1)
	}

	// Phase 2: policy engine + optional Redis cache (fail-open if Redis missing)
	polEngine := policy.NewEngine(pgStore, logger, cfg.PolicyRefreshInterval)
	go polEngine.Start(ctx)

	var cacheCoord *cache.Coordinator
	if cfg.CacheEnabled {
		rs, rerr := cache.NewRedisStore(ctx, cache.Options{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
		})
		if rerr != nil {
			logger.Warn("redis init failed; cache disabled (passthrough only)", "error", rerr)
			cacheCoord = cache.NewCoordinator(cache.NoopStore{})
		} else {
			if perr := rs.Ping(ctx); perr != nil {
				logger.Warn("redis ping failed at startup; continuing fail-open", "addr", cfg.RedisAddr, "error", perr)
			} else {
				logger.Info("redis cache ready", "addr", cfg.RedisAddr)
			}
			cacheCoord = cache.NewCoordinator(rs)
			defer rs.Close()
		}
	} else {
		cacheCoord = cache.NewCoordinator(cache.NoopStore{})
	}

	validator := auth.NewValidator(pgStore)
	pools := pool.NewManager(pgStore, cryptoCfg, cfg, logger)
	pools.StartIdleEviction(ctx)
	cursors := cursor.NewRegistry(cfg.CursorIdleTimeout)
	cachedCursors := cachedcursor.NewStore(cfg.CursorIdleTimeout, cfg.CachedCursorMaxBytes)
	limiter := ratelimit.New(cfg.TenantQPS, cfg.TenantBurst)
	cacheStats := cachestats.NewTracker()
	proxySrv := server.New(cfg, logger, validator, pools, cursors, server.Options{
		Cache:         cacheCoord,
		Policies:      polEngine,
		CacheStats:    cacheStats,
		CachedCursors: cachedCursors,
		Limiter:       limiter,
	})

	// HTTP health/metrics sidecar
	hs := &health.Server{
		Addr:       cfg.HealthAddr,
		CacheStats: cacheStats,
		ReadyFn: func(c context.Context) error {
			// Ready if we can ping postgres (Redis optional — fail-open)
			_, err := pgStore.ListTenants(c)
			return err
		},
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("health server starting", "addr", cfg.HealthAddr)
		if err := hs.ListenAndServe(ctx); err != nil {
			errCh <- err
		}
	}()
	go func() {
		if err := proxySrv.ListenAndServe(ctx); err != nil {
			errCh <- err
		}
	}()

	regCfg := region.LoadFromEnv()
	_ = regCfg // affinity available for future miss-forwarding

	logger.Info("nance proxy starting",
		"listen", cfg.ListenAddr,
		"health", cfg.HealthAddr,
		"region", regCfg.LocalRegion,
		"cache_enabled", cfg.CacheEnabled,
		"redis", cfg.RedisAddr,
		"note", "clients must use authMechanism=PLAIN&authSource=$external; username=tenantId, password=rawToken",
	)

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("server error", "error", err)
		}
	}

	// Graceful teardown
	shutdownCtx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer scancel()
	_ = proxySrv.Close()
	pools.DisconnectAll(shutdownCtx)
	logger.Info("nance proxy stopped")
}
