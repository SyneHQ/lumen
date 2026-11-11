package pg

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/SyneHQ/lumen/migrations"
)

// Run with LUMEN_TEST_PG_DSN pointing at a disposable Postgres.
func TestWebKeysAndSaltsLive(t *testing.T) {
	dsn := os.Getenv("LUMEN_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("Set LUMEN_TEST_PG_DSN to a disposable Postgres")
	}
	ctx := context.Background()
	s, err := NewStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrate := func(names ...string) {
		for _, n := range names {
			raw, err := migrations.FS.ReadFile("pg/" + n)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RunMigrations(ctx, string(raw)); err != nil {
				t.Fatalf("%s: %v", n, err)
			}
		}
	}

	// A key that exists before the web-keys migration stays a server key.
	migrate("001_init.sql", "002_tenant_lifecycle.sql")
	if _, err := s.db.Exec(ctx, `INSERT INTO lumen_tenants (team_id, ch_user, lifecycle_state) VALUES ('t1','u1','active')`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAPIKey(ctx, []byte("old"), "lum_live_old", "old", "t1"); err == nil {
		t.Fatal("pre-migration insert should fail on missing columns")
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO lumen_api_keys (key_hash, key_prefix, team_id) VALUES ('old','lum_live_old','t1')`); err != nil {
		t.Fatal(err)
	}
	migrate("003_web_keys.sql", "003_web_keys.sql") // replay-safe
	old, err := s.GetKeyByHash(ctx, []byte("old"))
	if err != nil || old.Kind != "server" || len(old.AllowedOrigins) != 0 || old.TeamID != "t1" {
		t.Fatalf("legacy key: %+v %v", old, err)
	}

	if err := s.SaveAPIKeyOfKind(ctx, []byte("web"), "lum_live_web", "site", "t1", "web", []string{"example.com", "*.example.com"}); err != nil {
		t.Fatal(err)
	}
	web, err := s.GetKeyByHash(ctx, []byte("web"))
	if err != nil || web.Kind != "web" || len(web.AllowedOrigins) != 2 || web.AllowedOrigins[1] != "*.example.com" {
		t.Fatalf("web key: %+v %v", web, err)
	}
	keys, err := s.ListTeamKeys(ctx, "t1")
	if err != nil || len(keys) != 2 {
		t.Fatalf("list: %v %v", keys, err)
	}
	for _, k := range keys {
		if (k.KeyPrefix == "lum_live_web") != (k.Kind == "web") {
			t.Fatalf("kind in list wrong: %+v", k)
		}
	}
	if err := s.SaveAPIKeyOfKind(ctx, []byte("bad"), "lum_live_bad", "x", "t1", "mobile", nil); err == nil {
		t.Fatal("check constraint did not reject kind 'mobile'")
	}

	today := time.Now().UTC()
	if _, err := s.db.Exec(ctx, `INSERT INTO lumen_web_salts VALUES (current_date - 5, 'stale')`); err != nil {
		t.Fatal(err)
	}
	a, err := s.WebSalt(ctx, today)
	if err != nil || len(a) != 32 {
		t.Fatalf("salt: %x %v", a, err)
	}
	b, _ := s.WebSalt(ctx, today) // a second replica sees the same salt
	if !bytes.Equal(a, b) {
		t.Fatal("salt changed within a day")
	}
	c, _ := s.WebSalt(ctx, today.AddDate(0, 0, 1))
	if bytes.Equal(a, c) {
		t.Fatal("tomorrow reused today's salt")
	}
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM lumen_web_salts WHERE salt = 'stale'`).Scan(&n)
	if n != 0 {
		t.Fatal("stale salt not pruned")
	}
}
