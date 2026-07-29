package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestInfisicalSecretLoading(t *testing.T) {
	// Mock Infisical API server
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/auth/universal-auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "mock_infisical_bearer_token_12345",
		})
	})

	mux.HandleFunc("/api/v3/secrets/raw", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock_infisical_bearer_token_12345" {
			http.Error(w, "Unauthorized bearer token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"secrets": []map[string]string{
				{"secretKey": "CLICKHOUSE_DSN", "secretValue": "clickhouse://infisical-ch:9000/lumen"},
				{"secretKey": "POSTGRES_DSN", "secretValue": "postgres://infisical-pg:5432/lumen"},
				{"secretKey": "ADMIN_TOKEN", "secretValue": "infisical-secret-admin-token"},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Set Infisical environment variables pointing to mock server
	t.Setenv("INFISICAL_API_URL", ts.URL)
	t.Setenv("INFISICAL_CLIENT_ID", "mock-client-id")
	t.Setenv("INFISICAL_CLIENT_SECRET", "mock-client-secret")
	t.Setenv("INFISICAL_PROJECT_ID", "mock-project-id")
	t.Setenv("INFISICAL_ENV", "test")

	// Clear target env vars first
	os.Unsetenv("CLICKHOUSE_DSN")
	os.Unsetenv("POSTGRES_DSN")
	os.Unsetenv("ADMIN_TOKEN")

	// Load config which triggers Infisical secret fetching
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
