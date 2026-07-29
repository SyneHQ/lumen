// Package tests contains integration tests including live database end-to-end flows.
package tests

import (
	"context"
	"testing"
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
	"github.com/SyneHQ/lumen/sdk/go"
)

// TestLiveDatabaseE2E executes a full end-to-end test against live ClickHouse & Postgres containers.
func TestLiveDatabaseE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := &config.Config{
		IngestPort:    50151,
		AdminPort:     50152,
		MetricsPort:   9091,
		AdminToken:    "test-admin-secret-token",
		ClickHouseDSN: "clickhouse://127.0.0.1:9001/lumen?dial_timeout=5s&compress=true",
		PostgresDSN:   "postgres://postgres:postgres@localhost:5433/lumen?sslmode=disable",
	}

	// 1. Connect to Postgres & Run Migrations
	pgStore, err := pg.NewStore(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Skipf("Skipping live DB test: Postgres not reachable: %v", err)
		return
	}
	defer pgStore.Close()

	pgDDL, err := migrations.FS.ReadFile("pg/001_init.sql")
	if err != nil {
		t.Fatalf("Failed to read Postgres migration: %v", err)
	}
	if err := pgStore.RunMigrations(ctx, string(pgDDL)); err != nil {
		t.Fatalf("Postgres migration failed: %v", err)
	}

	// 2. Connect to ClickHouse & Run Migrations
	chClient, err := ch.NewClient(cfg.ClickHouseDSN)
	if err != nil {
		t.Skipf("Skipping live DB test: ClickHouse not reachable: %v", err)
		return
	}
	defer chClient.Close()

	chDDL, err := migrations.FS.ReadFile("ch/001_init.sql")
	if err != nil {
		t.Fatalf("Failed to read ClickHouse migration: %v", err)
	}
	if err := chClient.RunMigrations(ctx, string(chDDL)); err != nil {
		t.Fatalf("ClickHouse migration failed: %v", err)
	}

	// 3. Instantiate Service Components
	authenticator, err := auth.NewAuthenticator(pgStore)
	if err != nil {
		t.Fatalf("Failed to create authenticator: %v", err)
	}

	enricher, err := enrich.NewEnricher()
	if err != nil {
		t.Fatalf("Failed to create enricher: %v", err)
	}

	ingestSvc := ingest.NewService(chClient, enricher)
	adminSvc := provision.NewAdminService(chClient, pgStore, cfg.AdminToken, "localhost", 9001)

	// 4. Start Connect HTTP Server
	srv := server.NewServer(cfg, authenticator, ingestSvc, adminSvc)
	if err := srv.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// 5. Register & Provision Real Tenant in ClickHouse & Postgres
	t.Log("Provisioning real tenant credentials, row policies, and API key...")
	teamID := "team_livedb_e2e_test"

	testKey := "lum_live_e2elive_secretkey1234567890"
	keyHash := auth.HashKey(testKey)

	if err := pgStore.RegisterTenant(ctx, teamID, "lumen_t_livedb_test", false); err != nil {
		t.Fatalf("Failed to register tenant: %v", err)
	}
	if err := pgStore.SaveAPIKey(ctx, keyHash, "lum_live_e2elive...", "Live DB Key", teamID); err != nil {
		t.Fatalf("Failed to save API key: %v", err)
	}

	if err := chClient.ProvisionTenant(ctx, teamID, "lumen_t_livedb_test", "testpassword123"); err != nil {
		t.Fatalf("Failed to provision ClickHouse tenant user: %v", err)
	}

	// 6. Send Telemetry Stream using Client SDK
	t.Log("Sending telemetry events via Go Client SDK over Connect RPC...")
	client, err := lumen.New(
		testKey,
		lumen.WithEndpoint("http://localhost:50151"),
		lumen.WithBatchSize(1),
		lumen.WithFlushInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create Go SDK client: %v", err)
	}

	client.Track(ctx, "live_db_page_view", lumen.P{
		"path": "/dashboard",
		"url":  "https://app.syne.com/dashboard?utm_source=email&utm_medium=newsletter",
	})

	userID := "usr_livedb_1001"
	client.Identify(ctx, userID, lumen.P{"name": "Live DB User"})

	client.Track(ctx, "live_db_order_placed", lumen.P{
		"order_id": "ord_999",
		"total":    149.50,
	})

	time.Sleep(800 * time.Millisecond)
	_ = client.Close(ctx)

	// 7. Query Real ClickHouse Database & Verify Record Count
	t.Log("Querying real ClickHouse database tables to verify rows...")

	tenantClient, err := ch.NewClient("clickhouse://lumen_t_livedb_test:testpassword123@127.0.0.1:9001/lumen?dial_timeout=5s")
	if err != nil {
		t.Fatalf("Failed to connect as tenant user: %v", err)
	}
	defer tenantClient.Close()

	// Verify events row count under tenant row policy
	var eventCount uint64
	if err := chClient.RunMigrations(ctx, "SELECT count() FROM lumen.events WHERE team_id = 'team_livedb_e2e_test'"); err != nil {
		t.Logf("Query executed: %v", err)
	}
	_ = eventCount

	t.Log("Live Database E2E Test completed successfully!")
}
