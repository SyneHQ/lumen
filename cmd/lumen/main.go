// Command lumen runs the Lumen event ingestion service.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

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

func main() {
	log.Println("Starting Lumen Event Ingestion Service...")

	// 1. Load Environmental Configuration
	cfg := config.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 2. Initialize Postgres Control Plane Store
	pgStore, err := pg.NewStore(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Printf("Warning: Postgres initial connection failed (%v). Retrying on request.", err)
	} else {
		defer pgStore.Close()
		log.Println("Connected to Postgres control plane database.")

		// Apply Postgres SQL migrations
		if pgDDL, err := migrations.FS.ReadFile("pg/001_init.sql"); err == nil {
			if err := pgStore.RunMigrations(context.Background(), string(pgDDL)); err != nil {
				log.Printf("Postgres migration warning: %v", err)
			} else {
				log.Println("Applied Postgres control plane migrations.")
			}
		}
	}

	// 3. Initialize ClickHouse Client
	chClient, err := ch.NewClient(cfg.ClickHouseDSN)
	if err != nil {
		log.Printf("Warning: ClickHouse initial connection failed (%v). Ingest will retry.", err)
	} else {
		defer chClient.Close()
		log.Println("Connected to ClickHouse database.")

		// Apply ClickHouse DDL migrations
		if chDDL, err := migrations.FS.ReadFile("ch/001_init.sql"); err == nil {
			if err := chClient.RunMigrations(context.Background(), string(chDDL)); err != nil {
				log.Printf("ClickHouse migration warning: %v", err)
			} else {
				log.Println("Applied ClickHouse event store migrations.")
			}
		}
	}

	// 4. Initialize Auth Interceptor with Ristretto Caching
	authenticator, err := auth.NewAuthenticator(pgStore)
	if err != nil {
		log.Fatalf("Failed to initialize authentication subsystem: %v", err)
	}

	// 5. Initialize Server-Side User-Agent & URL Enricher
	enricher, err := enrich.NewEnricher()
	if err != nil {
		log.Fatalf("Failed to initialize enrichment subsystem: %v", err)
	}

	// 6. Instantiate Ingest & Admin Services
	ingestSvc := ingest.NewService(chClient, enricher)
	adminSvc := provision.NewAdminService(chClient, pgStore, cfg.AdminToken, "localhost", 9000)

	// 7. Setup & Launch Connect HTTP Servers
	srv := server.NewServer(cfg, authenticator, ingestSvc, adminSvc)
	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start server listeners: %v", err)
	}

	log.Printf("Lumen Service online! Public Ingest Port: %d | Admin Port: %d | Metrics: %d\n",
		cfg.IngestPort, cfg.AdminPort, cfg.MetricsPort)

	// 8. Graceful Shutdown on OS Signal
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	<-stopChan
	log.Println("Shutting down Lumen service gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error during shutdown: %v", err)
	} else {
		log.Println("Lumen service stopped successfully.")
	}
}
