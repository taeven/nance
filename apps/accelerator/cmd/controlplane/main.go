package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/taeven/nance/accelerator/internal/config"
	"github.com/taeven/nance/accelerator/internal/controlplane/api"
	"github.com/taeven/nance/accelerator/internal/controlplane/api/handlers"
	"github.com/taeven/nance/accelerator/internal/controlplane/service"
	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/crypto"
	"github.com/taeven/nance/accelerator/internal/proxy/cache"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Connect to Postgres
	pgStore, err := store.NewPostgresStore(ctx, cfg.GetDatabaseURL())
	if err != nil {
		logger.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer pgStore.Close()

	// 2. Run migrations (simple file-based runner for Phase 0)
	if err := runMigrationsDirect(ctx, cfg.MigrationDir, logger); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
	logger.Info("migrations applied successfully")

	// 3. Crypto (for encrypting real Mongo URIs)
	cryptoCfg, err := crypto.NewConfigFromEnv(os.Getenv)
	if err != nil {
		logger.Error("crypto init failed (set NANCE_MASTER_KEY)", "error", err)
		// We still allow the control plane to start for tenant management,
		// but backend operations will fail until the key is provided.
		cryptoCfg = &crypto.Config{} // will error on use
	}

	// 4. Services
	tenantSvc := service.NewTenantService(pgStore)
	backendSvc := service.NewBackendService(pgStore, cryptoCfg)
	policySvc := service.NewPolicyService(pgStore)
	// Optional Redis for explicit invalidation (same as proxies)
	if addr := os.Getenv("NANCE_REDIS_ADDR"); addr != "" {
		if rs, err := cache.NewRedisStore(ctx, cache.Options{Addr: addr, Password: os.Getenv("NANCE_REDIS_PASSWORD")}); err == nil {
			policySvc = policySvc.WithCache(&service.RedisInvalidator{Store: rs})
			defer rs.Close()
			logger.Info("control plane redis invalidator enabled", "addr", addr)
		} else {
			logger.Warn("redis unavailable for control plane invalidation", "error", err)
		}
	}
	tokenSvc := service.NewTokenService(pgStore)
	mailer := &service.LogMailer{Log: logger}
	authSvc := service.NewAuthService(pgStore, mailer, logger)
	orgSvc := service.NewOrgService(pgStore, mailer).WithInviteOnly(cfg.InviteOnly)
	if cfg.InviteOnly {
		logger.Info("invite-only mode enabled (NANCE_INVITE_ONLY): users cannot create organizations; join via invite only; platform admin can still bootstrap tenants")
	}

	// 5. HTTP server
	pub := cfg.PlatformPublic()
	handler := api.NewServer(tenantSvc, backendSvc, policySvc, tokenSvc, authSvc, orgSvc, handlers.PlatformPublic{
		InviteOnly:          pub.InviteOnly,
		AllowOrgCreation:    pub.AllowOrgCreation,
		AllowAdminBootstrap: pub.AllowAdminBootstrap,
	})

	srv := &http.Server{
		Addr:         cfg.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown error", "error", err)
		}
	}()

	logger.Info("control plane starting", "addr", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
	logger.Info("control plane stopped")
}

// runMigrationsDirect performs the actual work using a fresh connection.
func runMigrationsDirect(ctx context.Context, migrationDir string, logger *slog.Logger) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://nance:nance@localhost:5432/nance?sslmode=disable"
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect for migrations: %w", err)
	}
	defer conn.Close(ctx)

	files, err := filepath.Glob(filepath.Join(migrationDir, "*.up.sql"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		logger.Warn("no migration files found", "dir", migrationDir)
		return nil
	}

	for _, f := range files {
		logger.Info("applying migration", "file", filepath.Base(f))
		content, err := os.ReadFile(f)
		if err != nil {
			return err
		}

		statements := splitSQLStatements(string(content))
		for _, stmt := range statements {
			stmt = stripSQLComments(strings.TrimSpace(stmt))
			if stmt == "" {
				continue
			}
			if _, err := conn.Exec(ctx, stmt); err != nil {
				// Ignore "already exists" type errors for idempotency on repeated runs
				if strings.Contains(err.Error(), "already exists") ||
					strings.Contains(err.Error(), "duplicate key") ||
					(strings.Contains(err.Error(), "relation") && strings.Contains(err.Error(), "already exists")) {
					continue
				}
				return fmt.Errorf("exec %s: %w", f, err)
			}
		}
	}
	return nil
}

func splitSQLStatements(sql string) []string {
	// Very simple splitter: split on ; (good enough for our pure DDL migrations)
	parts := strings.Split(sql, ";")
	return parts
}

// stripSQLComments removes leading full-line -- comments so migrate headers
// like "-- +migrate Up" do not cause the entire first statement to be skipped.
func stripSQLComments(stmt string) string {
	lines := strings.Split(stmt, "\n")
	var kept []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
