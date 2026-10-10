package provision

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/ClickHouse/clickhouse-go/v2"
	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
	"github.com/SyneHQ/lumen/internal/ch"
	"github.com/SyneHQ/lumen/internal/pg"
	"github.com/SyneHQ/lumen/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CI explicitly enables this test against its disposable service containers.
// Each invocation owns a separate PG schema and CH database.
func TestTenantLifecycleLive(t *testing.T) {
	if os.Getenv("LUMEN_LIFECYCLE_LIVE") != "1" {
		t.Skip("Set LUMEN_LIFECYCLE_LIVE=1 with disposable CI databases")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	schema := "lifecycle_" + suffix
	database := "app_lifecycle_" + suffix
	pgDSN := "postgres://postgres:postgres@127.0.0.1:5433/lumen?sslmode=disable"
	rawPG, err := pgx.Connect(ctx, pgDSN)
	if err != nil {
		t.Fatal("connect fixture PG")
	}
	defer rawPG.Close(context.Background())
	if _, err = rawPG.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create isolated PG schema")
	}
	defer rawPG.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	uri, _ := url.Parse(pgDSN)
	q := uri.Query()
	q.Set("search_path", schema)
	q.Set("pool_max_conns", "1")
	uri.RawQuery = q.Encode()
	store, err := pg.NewStore(ctx, uri.String())
	if err != nil {
		t.Fatal("connect schema store")
	}
	defer store.Close()
	second, err := pg.NewStore(ctx, uri.String())
	if err != nil {
		t.Fatal("connect second store")
	}
	defer second.Close()
	// Model a pre-lifecycle installation before replaying current migrations.
	old := `CREATE TABLE lumen_tenants(team_id text PRIMARY KEY,ch_user text UNIQUE NOT NULL,created_at timestamptz DEFAULT now(),store_ip boolean NOT NULL DEFAULT false);
 CREATE TABLE lumen_api_keys(key_hash bytea PRIMARY KEY,key_prefix text NOT NULL,name text NOT NULL DEFAULT 'Default Ingestion Key',team_id text NOT NULL REFERENCES lumen_tenants(team_id),created_at timestamptz DEFAULT now(),revoked_at timestamptz);`
	if err = store.RunMigrations(ctx, old); err != nil {
		t.Fatal("create legacy PG tables")
	}
	files, _ := migrations.FS.ReadDir("pg")
	for repeat := 0; repeat < 2; repeat++ {
		for _, file := range files {
			raw, e := migrations.FS.ReadFile("pg/" + file.Name())
			if e != nil || store.RunMigrations(ctx, string(raw)) != nil {
				t.Fatal("replay PG lifecycle migrations")
			}
		}
	}
	native, err := clickhouse.Open(&clickhouse.Options{Addr: []string{"127.0.0.1:9001"}, Auth: clickhouse.Auth{Database: "lumen"}})
	if err != nil {
		t.Fatal("connect fixture CH")
	}
	defer native.Close()
	if err = native.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatal("create isolated CH database")
	}
	defer native.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database)
	client, err := ch.NewClientContext(ctx, "clickhouse://127.0.0.1:9001/"+database)
	if err != nil {
		t.Fatal("connect non-default database")
	}
	defer client.Close()
	files, _ = migrations.FS.ReadDir("ch")
	for _, file := range files {
		raw, e := migrations.FS.ReadFile("ch/" + file.Name())
		if e != nil || client.RunMigrations(ctx, string(raw)) != nil {
			t.Fatal("apply CH migrations")
		}
	}
	token := strings.Repeat("a", 40)
	service := NewAdminService(client, store, token, "localhost", 9001)
	provision := func(team string) *lumenv1.ProvisionResponse {
		t.Helper()
		req := connect.NewRequest(&lumenv1.ProvisionRequest{TeamId: team})
		req.Header().Set("Authorization", "Bearer "+token)
		response, e := service.Provision(ctx, req)
		if e != nil {
			t.Fatal("provision fixture tenant")
		}
		return response.Msg
	}
	remove := func(team string) {
		t.Helper()
		req := connect.NewRequest(&lumenv1.DeprovisionRequest{TeamId: team})
		req.Header().Set("Authorization", "Bearer "+token)
		if _, e := service.Deprovision(ctx, req); e != nil {
			t.Fatal("deprovision fixture tenant")
		}
	}
	main, survivor := "lifecycle_main_"+suffix, "lifecycle_survivor_"+suffix
	first := provision(main)
	other := provision(survivor)
	defer remove(main)
	defer remove(survivor)
	if first.Database != database {
		t.Fatal("provision response lost configured database")
	}
	// The schema migration can replay without turning an active tenant into legacy.
	ddl, _ := migrations.FS.ReadFile("pg/002_tenant_lifecycle.sql")
	if store.RunMigrations(ctx, string(ddl)) != nil {
		t.Fatal("replay active lifecycle migration")
	}
	row, err := store.GetTenant(ctx, main)
	if err != nil || row == nil || row.State != "active" {
		t.Fatal("migration changed active tenant state")
	}
	accessCounts := func(team, user string, wantUsers, wantPolicies, wantQuotas uint64) {
		t.Helper()
		queries := []struct {
			sql  string
			args []any
			want uint64
		}{
			{"SELECT count() FROM system.users WHERE name=?", []any{user}, wantUsers},
			{"SELECT count() FROM system.row_policies WHERE database=? AND short_name IN (?,?,?,?)", []any{database, "pol_ev_" + sanitizeSlug(team), "pol_sess_" + sanitizeSlug(team), "pol_ident_" + sanitizeSlug(team), "pol_pers_" + sanitizeSlug(team)}, wantPolicies},
			{"SELECT count() FROM system.quotas WHERE name=?", []any{"q_" + sanitizeSlug(team)}, wantQuotas},
		}
		for index, query := range queries {
			var count uint64
			if e := native.QueryRow(ctx, query.sql, query.args...).Scan(&count); e != nil || count != query.want {
				code := int32(0)
				var exception *clickhouse.Exception
				if errors.As(e, &exception) {
					code = exception.Code
				}
				t.Fatalf("CH access metadata check %d: count=%d want=%d query_failed=%t exception_code=%d", index, count, query.want, e != nil, code)
			}
		}
	}
	accessCounts(main, first.Username, 1, 4, 1)
	remove(main)
	remove(main)
	row, err = store.GetTenant(ctx, main)
	if err != nil || row == nil || row.State != "inactive" || row.ActiveKeyCount != 0 {
		t.Fatal("delete did not persist inactive state and key revocation")
	}
	accessCounts(main, first.Username, 0, 0, 0)
	accessCounts(survivor, other.Username, 1, 4, 1)
	// Restart uses a separate store instance and the exact startup reconciler.
	if err = ReconcileTenantAccess(ctx, second, client); err != nil {
		t.Fatal("restart after deletion failed")
	}
	accessCounts(main, first.Username, 0, 0, 0)
	accessCounts(survivor, other.Username, 1, 4, 1)
	again := provision(main)
	if again.Password == first.Password || again.IngestKey == first.IngestKey {
		t.Fatal("reprovision reused credentials")
	}
	row, err = store.GetTenant(ctx, main)
	if err != nil || row == nil || row.State != "active" || row.ActiveKeyCount != 1 || row.KeyCount != 2 {
		t.Fatal("reprovision revived old key history")
	}
	// The unique PG username reservation fails before another team's CH mutation.
	collision := strings.Replace(main, "_", "-", 1)
	req := connect.NewRequest(&lumenv1.ProvisionRequest{TeamId: collision})
	req.Header().Set("Authorization", "Bearer "+token)
	if _, err = service.Provision(ctx, req); err == nil {
		t.Fatal("colliding tenant username was accepted")
	}
	accessCounts(main, again.Username, 1, 4, 1)
	// Persist deletion intent, then simulate a process exit before CH cleanup.
	err = store.WithTenantLock(ctx, main, func(locked *pg.Store) error { return locked.BeginDeletion(ctx, main, "test_interrupted_delete") })
	if err != nil {
		t.Fatal("persist interrupted deletion")
	}
	row, err = second.GetTenant(ctx, main)
	if err != nil || row == nil || row.State != "deleting" || row.ActiveKeyCount != 0 {
		t.Fatal("second instance cannot see deletion intent")
	}
	if err = ReconcileTenantAccess(ctx, second, client); err != nil {
		t.Fatal("restart did not finish pending cleanup")
	}
	accessCounts(main, again.Username, 0, 0, 0)
	// Interrupted provisioning is never silently activated after restart.
	interrupted := "lifecycle_interrupted_" + suffix
	err = store.WithTenantLock(ctx, interrupted, func(locked *pg.Store) error {
		if err := locked.PrepareProvision(ctx, interrupted, "lumen_t_"+interrupted, false); err != nil {
			return err
		}
		if err := client.CreateTenantUser(ctx, "lumen_t_"+interrupted, "fixture-ownership-password"); err != nil {
			return err
		}
		return locked.ConfirmTenantCreated(ctx, interrupted)
	})
	if err != nil {
		t.Fatal("persist interrupted provision")
	}
	if err = ReconcileTenantAccess(ctx, second, client); err != nil {
		t.Fatal("restart did not clean pending provision")
	}
	row, err = second.GetTenant(ctx, interrupted)
	if err != nil || row == nil || row.State != "inactive" {
		t.Fatal("pending provision was activated")
	}
	if err = RecoverLegacyDeletion(ctx, second, client, []string{interrupted}); err == nil {
		t.Fatal("legacy recovery accepted empty key history")
	}
	// Two independent pools contend on the same session advisory lock. The first
	// callback reads through its reserved connection despite pool_max_conns=1.
	entered, release, done, secondEntered := make(chan struct{}), make(chan struct{}), make(chan error, 1), make(chan struct{})
	go func() {
		done <- store.WithTenantLock(ctx, survivor, func(locked *pg.Store) error {
			if _, e := locked.GetTenant(ctx, survivor); e != nil {
				return e
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first lock callback did not enter")
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.WithTenantLock(ctx, survivor, func(*pg.Store) error { close(secondEntered); return nil })
	}()
	select {
	case <-secondEntered:
		close(release)
		t.Fatal("second instance bypassed tenant lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if <-done != nil || <-secondDone != nil {
		t.Fatal("tenant lock completion failed")
	}
	// The exact old deprovision representation requires explicit operator recovery.
	// Startup does not infer deletion from revoked key history.
	if _, err = rawPG.Exec(ctx, fmt.Sprintf("UPDATE %s.lumen_tenants SET lifecycle_state='legacy' WHERE team_id=$1", schema), main); err != nil {
		t.Fatal("stage legacy metadata")
	}
	if err = ReconcileTenantAccess(ctx, second, client); !errors.Is(err, ErrLegacyRepairRequired) {
		t.Fatal("legacy deletion was guessed during startup")
	}
	if err = RecoverLegacyDeletion(ctx, second, client, []string{survivor}); err == nil {
		t.Fatal("legacy recovery accepted an active unrelated tenant")
	}
	accessCounts(survivor, other.Username, 1, 4, 1)
	if err = RecoverLegacyDeletion(ctx, second, client, []string{main}); err != nil {
		t.Fatal("explicit legacy recovery failed")
	}
	if err = RecoverLegacyDeletion(ctx, second, client, []string{main}); err != nil {
		t.Fatal("legacy recovery retry was not idempotent")
	}
	if err = ReconcileTenantAccess(ctx, second, client); err != nil {
		t.Fatal("startup failed after explicit legacy recovery")
	}
	var recoveryReason string
	if err = rawPG.QueryRow(ctx, fmt.Sprintf("SELECT legacy_recovery_reason FROM %s.lumen_tenants WHERE team_id=$1", schema), main).Scan(&recoveryReason); err != nil || recoveryReason != "operator_confirmed_legacy_deletion" {
		t.Fatal("legacy recovery audit marker missing")
	}
	recovered := provision(main)
	remove(main)
	if err = rawPG.QueryRow(ctx, fmt.Sprintf("SELECT legacy_recovery_reason FROM %s.lumen_tenants WHERE team_id=$1", schema), main).Scan(&recoveryReason); err != nil || recoveryReason != "operator_confirmed_legacy_deletion" {
		t.Fatal("reprovision changed immutable recovery marker")
	}
	accessCounts(main, recovered.Username, 0, 0, 0)
	row, err = second.GetTenant(ctx, main)
	if err != nil || row == nil || row.State != "inactive" {
		t.Fatal("legacy removed tenant was not retained as inactive")
	}
	accessCounts(survivor, other.Username, 1, 4, 1)
}
