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
	"github.com/taeven/nance/accelerator/internal/proxy/auth"
	proxyconfig "github.com/taeven/nance/accelerator/internal/proxy/config"
	"github.com/taeven/nance/accelerator/internal/proxy/cursor"
	"github.com/taeven/nance/accelerator/internal/proxy/health"
	"github.com/taeven/nance/accelerator/internal/proxy/pool"
	"github.com/taeven/nance/accelerator/internal/proxy/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

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

	validator := auth.NewValidator(pgStore)
	pools := pool.NewManager(pgStore, cryptoCfg, cfg, logger)
	cursors := cursor.NewRegistry(cfg.CursorIdleTimeout)
	proxySrv := server.New(cfg, logger, validator, pools, cursors)

	// HTTP health/metrics sidecar
	hs := &health.Server{
		Addr: cfg.HealthAddr,
		ReadyFn: func(c context.Context) error {
			// Ready if we can ping postgres
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

	logger.Info("nance proxy starting",
		"listen", cfg.ListenAddr,
		"health", cfg.HealthAddr,
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
