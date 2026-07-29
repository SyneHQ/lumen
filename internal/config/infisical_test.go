package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestInfisicalSecretLoading(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/auth/universal-auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "mock_infisical_bearer_token_12345",
			"expiresIn":   7200,
			"tokenType":   "Bearer",
		})
	})

	mux.HandleFunc("/api/v3/secrets/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"secrets": []map[string]any{
				{
					"id":          "sec-1",
					"secretKey":   "CLICKHOUSE_DSN",
					"secretValue": "clickhouse://infisical-ch:9000/lumen",
				},
				{
					"id":          "sec-2",
					"secretKey":   "POSTGRES_DSN",
					"secretValue": "postgres://infisical-pg:5432/lumen",
				},
				{
					"id":          "sec-3",
					"secretKey":   "ADMIN_TOKEN",
					"secretValue": "infisical-secret-admin-token",
				},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	t.Setenv("INFISICAL_API_URL", ts.URL)
	t.Setenv("INFISICAL_CLIENT_ID", "mock-client-id")
	t.Setenv("INFISICAL_CLIENT_SECRET", "mock-client-secret")
	t.Setenv("INFISICAL_PROJECT_ID", "mock-project-id")
	t.Setenv("INFISICAL_ENV", "test")

	os.Unsetenv("CLICKHOUSE_DSN")
	os.Unsetenv("POSTGRES_DSN")
	os.Unsetenv("ADMIN_TOKEN")

	cfg := Load()

	if cfg.ClickHouseDSN != "clickhouse://infisical-ch:9000/lumen" {
		t.Errorf("Expected ClickHouseDSN from Infisical, got %s", cfg.ClickHouseDSN)
	}

	if cfg.PostgresDSN != "postgres://infisical-pg:5432/lumen" {
		t.Errorf("Expected PostgresDSN from Infisical, got %s", cfg.PostgresDSN)
	}

	if cfg.AdminToken != "infisical-secret-admin-token" {
		t.Errorf("Expected AdminToken from Infisical, got %s", cfg.AdminToken)
	}
}
