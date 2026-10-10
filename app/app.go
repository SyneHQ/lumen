// Package app wires and runs the Lumen service.
//
// It exists as an exported package (rather than living inside package main) so
// that alternative binaries — notably the commercial enterprise build in a
// separate module — can boot the same engine while injecting their own ee.Hooks.
// See OPEN_CORE.md.
package app

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/SyneHQ/lumen/ee"
	"github.com/SyneHQ/lumen/internal/auth"
	"github.com/SyneHQ/lumen/internal/ch"
	"github.com/SyneHQ/lumen/internal/config"
	"github.com/SyneHQ/lumen/internal/enrich"
	"github.com/SyneHQ/lumen/internal/ingest"
	"github.com/SyneHQ/lumen/internal/pg"
	"github.com/SyneHQ/lumen/internal/provision"
	"github.com/SyneHQ/lumen/internal/server"
	"github.com/SyneHQ/lumen/migrations"
)

// Options configures a Lumen run.
type Options struct {
	// Config is the resolved configuration. If nil, config.Load() is used.
	Config *config.Config

	// Hooks supplies commercial extension points. The zero value is the
	// community no-op set, so leaving this empty yields the open-source build.
	Hooks ee.Hooks
}

// Run boots the service and blocks until the context is cancelled or the
// process receives SIGINT or SIGTERM, then shuts down gracefully.
func Run(ctx context.Context, opts Options) error {
	cfg := opts.Config
	if cfg == nil {
		cfg = config.Load()
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	hooks := opts.Hooks.Normalize()
	ent := hooks.Licensor.Entitlements()

	log.Printf("Starting Lumen Event Ingestion Service (edition=%s)...", ent.Edition)
	if ent.LicensedTo != "" {
		log.Printf("Licensed to: %s", ent.LicensedTo)
	}

	initCtx, cancelInit := context.WithTimeout(ctx, 90*time.Second)
	defer cancelInit()
	pgStore, err := connectWithRetry(initCtx, time.Second, func(attempt context.Context) (*pg.Store, error) {
		return pg.NewStore(attempt, cfg.PostgresDSN)
	})
	if err != nil {
		return fmt.Errorf("PostgreSQL startup connection failed: %w", err)
	}
	defer pgStore.Close()
	log.Println("Connected to Postgres control plane database.")
	if err := applyMigrations(initCtx, "pg", func(sql string) error { return pgStore.RunMigrations(initCtx, sql) }, "PostgreSQL"); err != nil {
		return err
	}
	chClient, err := connectWithRetry(initCtx, time.Second, func(attempt context.Context) (*ch.Client, error) {
		return ch.NewClientContextWithOptions(attempt, cfg.ClickHouseDSN, ch.ClientOptions{Compression: cfg.ClickHouseCompression})
	})
	if err != nil {
		return fmt.Errorf("ClickHouse startup connection failed: %w", err)
	}
	defer chClient.Close()
	log.Println("Connected to ClickHouse database.")
	if err := applyMigrations(initCtx, "ch", func(sql string) error { return chClient.RunMigrations(initCtx, sql) }, "ClickHouse"); err != nil {
		return err
	}
	tenants, err := pgStore.ListTenants(initCtx)
	if err != nil {
		return fmt.Errorf("startup tenant access reconciliation failed")
	}
	for _, tenant := range tenants {
		if err := chClient.EnsureTenantAccess(initCtx, tenant.TeamID, tenant.CHUser); err != nil {
			return fmt.Errorf("startup tenant access reconciliation failed")
		}
	}

	// 3. Auth interceptor with key cache
	authenticator, err := auth.NewAuthenticator(pgStore)
	if err != nil {
		return fmt.Errorf("initialize authentication subsystem: %w", err)
	}

	// 4. Server-side enrichment
	enricher, err := enrich.NewEnricher(cfg.GeoIPDBPath)
	if err != nil {
		return fmt.Errorf("initialize enrichment subsystem: %w", err)
	}
	defer enricher.Close()

	// 5. Services
	ingestSvc := ingest.NewService(chClient, enricher, hooks)
	adminSvc := provision.NewAdminService(chClient, pgStore, cfg.AdminToken, cfg.CHHost, chClient.NativePort())

	// 6. Listeners
	srv := server.NewServer(cfg, authenticator, ingestSvc, adminSvc)
	if err := srv.Start(); err != nil {
		return fmt.Errorf("start server listeners: %w", err)
	}

	log.Printf("Lumen Service online! Public Ingest Port: %d | Admin Port: %d | Metrics: %d",
		cfg.IngestPort, cfg.AdminPort, cfg.MetricsPort)

	// 7. Wait for shutdown signal
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopChan)

	select {
	case <-stopChan:
		log.Println("Shutting down Lumen service gracefully...")
	case <-ctx.Done():
		log.Println("Context cancelled, shutting down Lumen service gracefully...")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Println("Lumen service stopped successfully.")
	return nil
}

// applyMigrations runs every embedded .sql file under dir (e.g. "ch" or "pg")
// in lexicographic order. Files are applied unconditionally; each one must be
// idempotent (IF NOT EXISTS / CREATE OR REPLACE), which also makes boot-time
// re-application safe.
func applyMigrations(ctx context.Context, dir string, exec func(string) error, label string) error {
	entries, err := fs.ReadDir(migrations.FS, dir)
	if err != nil {
		return fmt.Errorf("cannot list %s migrations", label)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		sql, err := migrations.FS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("cannot read %s migration %s", label, name)
		}
		if err := exec(string(sql)); err != nil {
			return fmt.Errorf("%s migration %s failed", label, name)
		}
	}
	log.Printf("Applied %s migrations (%d files).", label, len(names))
	return nil
}
