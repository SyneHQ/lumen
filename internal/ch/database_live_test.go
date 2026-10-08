package ch

import (
	"context"
	"github.com/SyneHQ/lumen/migrations"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestManagedDatabaseMigrations(t *testing.T) {
	dsn := os.Getenv("LUMEN_TEST_MANAGED_DSN")
	if dsn == "" {
		t.Skip("Set LUMEN_TEST_MANAGED_DSN to a disposable managed-profile database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c, err := NewClientContext(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if c.NativePort() != 9000 && c.NativePort() != 9440 {
		t.Fatal("fixture native port must be 9000 or 9440")
	}
	if c.database != "app" {
		t.Fatal("fixture must use the app database")
	}
	files, err := migrations.FS.ReadDir("ch")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := migrations.FS.ReadFile("ch/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err = c.RunMigrations(ctx, string(raw)); err != nil {
			t.Fatalf("%s: %v", file.Name(), err)
		}
	}
	if err = c.ProvisionTenant(ctx, "managed_test_a", "lumen_t_managed_test_a", "fixture_password_a"); err != nil {
		t.Fatal(err)
	}
	defer c.DeprovisionTenant(ctx, "managed_test_a", "lumen_t_managed_test_a")
	if err = c.InsertIdentity(ctx, "managed_test_a", "anonymous_a", "user_a"); err != nil {
		t.Fatal(err)
	}
	if err = c.InsertIdentity(ctx, "managed_test_b", "anonymous_b", "user_b"); err != nil {
		t.Fatal(err)
	}
	readerURL := *parsed
	readerURL.User = url.UserPassword("lumen_t_managed_test_a", "fixture_password_a")
	reader, err := NewClientContext(ctx, readerURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var visible uint64
	if err = reader.conn.QueryRow(ctx, "SELECT count() FROM app.identities").Scan(&visible); err != nil || visible != 1 {
		t.Fatalf("tenant isolation: count=%d error=%v", visible, err)
	}
	if err = reader.conn.Exec(ctx, "INSERT INTO app.identities (team_id, anon_id, user_id) VALUES ('managed_test_a', 'forbidden', 'forbidden')"); err == nil {
		t.Fatal("tenant reader can write")
	}
	var count uint64
	if err = c.conn.QueryRow(ctx, "SELECT count() FROM app.identities").Scan(&count); err != nil || count != 2 {
		t.Fatalf("identity insert: %d %v", count, err)
	}
}
