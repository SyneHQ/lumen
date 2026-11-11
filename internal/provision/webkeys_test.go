package provision

import (
	"context"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"

	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
	"github.com/SyneHQ/lumen/internal/pg"
	"github.com/SyneHQ/lumen/migrations"
)

func createKey(a *AdminService, msg *lumenv1.CreateKeyRequest) (*connect.Response[lumenv1.CreateKeyResponse], error) {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer secret-token")
	return a.CreateKey(context.Background(), req)
}

func TestCreateKeyRejectsBadWebKeys(t *testing.T) {
	a := NewAdminService(nil, nil, "secret-token", "localhost", 9000) // no store: validation must fail first
	for _, msg := range []*lumenv1.CreateKeyRequest{
		{TeamId: "t", Kind: "web"},
		{TeamId: "t", Kind: "web", AllowedOrigins: []string{"https://example.com"}},
		{TeamId: "t", Kind: "web", AllowedOrigins: []string{"*.com"}},
		{TeamId: "t", AllowedOrigins: []string{"example.com"}},
		{TeamId: "t", Kind: "mobile"},
	} {
		if _, err := createKey(a, msg); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%v: got %v, want invalid_argument", msg, err)
		}
	}
}

// Run with LUMEN_TEST_PG_DSN pointing at a disposable Postgres.
func TestCreateAndListWebKeyLive(t *testing.T) {
	dsn := os.Getenv("LUMEN_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("Set LUMEN_TEST_PG_DSN to a disposable Postgres")
	}
	ctx := context.Background()
	store, err := pg.NewStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, n := range []string{"001_init.sql", "002_tenant_lifecycle.sql", "003_web_keys.sql"} {
		raw, _ := migrations.FS.ReadFile("pg/" + n)
		if err := store.RunMigrations(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RunMigrations(ctx, `INSERT INTO lumen_tenants (team_id, ch_user, lifecycle_state) VALUES ('tp','up','active') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	a := NewAdminService(nil, store, "secret-token", "localhost", 9000)

	web, err := createKey(a, &lumenv1.CreateKeyRequest{TeamId: "tp", Name: "site", Kind: "web", AllowedOrigins: []string{"Example.com", "*.example.com"}})
	if err != nil || web.Msg.Kind != "web" || strings.Join(web.Msg.AllowedOrigins, ",") != "example.com,*.example.com" {
		t.Fatalf("web create: %v %v", web, err)
	}
	srv, err := createKey(a, &lumenv1.CreateKeyRequest{TeamId: "tp", Name: "backend"})
	if err != nil || srv.Msg.Kind != "server" || len(srv.Msg.AllowedOrigins) != 0 || !strings.HasPrefix(srv.Msg.IngestKey, "lum_live_") {
		t.Fatalf("server create: %v %v", srv, err)
	}

	req := connect.NewRequest(&lumenv1.ListKeysRequest{TeamId: "tp"})
	req.Header().Set("Authorization", "Bearer secret-token")
	list, err := a.ListKeys(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*lumenv1.KeyMetadata{}
	for _, k := range list.Msg.Keys {
		got[k.KeyPrefix] = k
	}
	if k := got[web.Msg.KeyPrefix]; k == nil || k.Kind != "web" || len(k.AllowedOrigins) != 2 {
		t.Fatalf("web key listed as %v", k)
	}
	if k := got[srv.Msg.KeyPrefix]; k == nil || k.Kind != "server" || len(k.AllowedOrigins) != 0 || !k.IsActive {
		t.Fatalf("server key listed as %v", k)
	}

	// RotateKey replaces server keys and leaves web keys alone.
	rot := connect.NewRequest(&lumenv1.RotateKeyRequest{TeamId: "tp"})
	rot.Header().Set("Authorization", "Bearer secret-token")
	rotated, err := a.RotateKey(ctx, rot)
	if err != nil {
		t.Fatal(err)
	}
	list, err = a.ListKeys(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	activeServer := 0
	for _, k := range list.Msg.Keys {
		switch {
		case k.KeyPrefix == web.Msg.KeyPrefix && !k.IsActive:
			t.Fatal("rotation revoked the web key")
		case k.KeyPrefix == srv.Msg.KeyPrefix && k.IsActive:
			t.Fatal("rotation kept the old server key")
		case k.Kind == "server" && k.IsActive:
			activeServer++
			if !strings.HasPrefix(rotated.Msg.NewIngestKey, strings.TrimSuffix(k.KeyPrefix, "...")) {
				t.Fatalf("active server key %s is not the rotated one", k.KeyPrefix)
			}
		}
	}
	if activeServer != 1 {
		t.Fatalf("%d active server keys after rotation", activeServer)
	}
}
