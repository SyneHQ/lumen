package config

import (
	"os"
	"strings"
	"testing"
)

func TestClickHouseDSNSourcePrecedence(t *testing.T) {
	const envDSN = "clickhouse://env-user:env-secret@environment:9440/app?secure=true"
	const vaultDSN = "clickhouse://vault-user:vault-secret@infisical:9440/app?secure=true"
	for _, tc := range []struct{ name, source, environment, secret, want string }{
		{"default keeps infisical first", "", envDSN, vaultDSN, vaultDSN},
		{"explicit infisical first", "infisical", envDSN, vaultDSN, vaultDSN},
		{"default falls back to environment", "", envDSN, "", envDSN},
		{"explicit infisical keeps legacy fallback", "infisical", envDSN, "", envDSN},
		{"environment overrides secret", "environment", envDSN, vaultDSN, envDSN},
		{"environment missing ignores secret", "environment", "", vaultDSN, ""},
		{"environment missing has no development fallback", "environment", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLICKHOUSE_DSN_SOURCE", tc.source)
			t.Setenv("CLICKHOUSE_DSN", tc.environment)
			t.Setenv("CLICKHOUSE_COMPRESSION", "")
			cfg := loadWithSecrets(map[string]string{"CLICKHOUSE_DSN": tc.secret, "POSTGRES_DSN": "postgres://secret-only/db"})
			if cfg.ClickHouseDSN != tc.want {
				t.Fatal("selected an unexpected credential source")
			}
			if cfg.PostgresDSN != "postgres://secret-only/db" {
				t.Fatal("changed unrelated secret loading")
			}
			if os.Getenv("CLICKHOUSE_DSN") != tc.environment {
				t.Fatal("secret loading changed the process environment")
			}
			cfg.AdminToken = strings.Repeat("a", minAdminTokenLen)
			err := cfg.Validate()
			wantErr := tc.source == "environment" && tc.environment == ""
			if (err != nil) != wantErr {
				t.Fatalf("validation mismatch: %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "vault-secret") || strings.Contains(err.Error(), "env-secret")) {
				t.Fatal("error exposed credentials")
			}
		})
	}
}

func TestClickHouseConfigurationDefaults(t *testing.T) {
	t.Setenv("CLICKHOUSE_DSN_SOURCE", "")
	t.Setenv("CLICKHOUSE_DSN", "")
	t.Setenv("CLICKHOUSE_COMPRESSION", "")
	cfg := loadWithSecrets(nil)
	if cfg.ClickHouseDSNSource != "infisical" || cfg.ClickHouseCompression != "" {
		t.Fatal("changed the default configuration")
	}
	if cfg.ClickHouseDSN != "clickhouse://127.0.0.1:9000/lumen?dial_timeout=10s&compress=true" {
		t.Fatal("changed the legacy development DSN")
	}
}

func TestClickHouseExplicitOptionsRejectInvalidValuesInDevMode(t *testing.T) {
	for _, tc := range []struct{ name, source, dsn, compression string }{
		{"unknown source", "automatic", "clickhouse://host/app", ""},
		{"source is case sensitive", "Environment", "clickhouse://host/app", ""},
		{"blank environment DSN", "environment", " \t\n", ""},
		{"unsupported compression", "environment", "clickhouse://host/app", "zstd"},
		{"compression is case sensitive", "environment", "clickhouse://host/app", "LZ4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.AdminToken = ""
			cfg.DevMode = true
			cfg.ClickHouseDSNSource = tc.source
			cfg.ClickHouseDSN = tc.dsn
			cfg.ClickHouseCompression = tc.compression
			if err := cfg.Validate(); err == nil {
				t.Fatal("development mode bypassed connection option validation")
			}
			if cfg.AdminToken != "" {
				t.Fatal("generated a token before rejecting invalid options")
			}
		})
	}
}

func TestClickHouseCompressionIsExplicitEnvironmentOption(t *testing.T) {
	t.Setenv("CLICKHOUSE_COMPRESSION", "lz4")
	t.Setenv("CLICKHOUSE_DSN_SOURCE", "environment")
	t.Setenv("CLICKHOUSE_DSN", "clickhouse://bound/app?compress=zstd")
	cfg := loadWithSecrets(map[string]string{"CLICKHOUSE_COMPRESSION": "none", "CLICKHOUSE_DSN_SOURCE": "infisical", "CLICKHOUSE_DSN": "clickhouse://other/app"})
	if cfg.ClickHouseCompression != "lz4" || cfg.ClickHouseDSNSource != "environment" || cfg.ClickHouseDSN != "clickhouse://bound/app?compress=zstd" {
		t.Fatal("Infisical replaced the selected source or explicit compression option")
	}
	cfg.AdminToken = strings.Repeat("a", minAdminTokenLen)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
