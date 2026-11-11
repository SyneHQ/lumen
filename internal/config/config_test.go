package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDefaults(t *testing.T) {
	cfg := Load()

	if cfg.IngestPort != 50051 {
		t.Errorf("Expected default IngestPort 50051, got %d", cfg.IngestPort)
	}

	if cfg.AdminPort != 50052 {
		t.Errorf("Expected default AdminPort 50052, got %d", cfg.AdminPort)
	}

	if cfg.MetricsPort != 9090 {
		t.Errorf("Expected default MetricsPort 9090, got %d", cfg.MetricsPort)
	}
}

func TestConfigEnvOverride(t *testing.T) {
	os.Setenv("INGEST_PORT", "60051")
	defer os.Unsetenv("INGEST_PORT")

	cfg := Load()
	if cfg.IngestPort != 60051 {
		t.Errorf("Expected overridden IngestPort 60051, got %d", cfg.IngestPort)
	}
}

func TestAdvertisedClickHouseHostConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, environment, secret, want string
	}{
		{"derive_when_unset", "", "", ""},
		{"environment_override", "public.example.test", "", "public.example.test"},
		{"secret_override", "", "secret.example.test", "secret.example.test"},
		{"preserve_secret_precedence", "environment.example.test", "secret.example.test", "secret.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CH_HOST", tc.environment)
			cfg := loadWithSecrets(map[string]string{"CH_HOST": tc.secret})
			if cfg.CHHost != tc.want {
				t.Fatal("advertised host did not preserve configuration precedence")
			}
		})
	}
}

func TestGeoIPSourceConfig(t *testing.T) {
	t.Setenv("GEOIP_SOURCE", "")
	t.Setenv("GEOIP_CACHE_DIR", "")
	cfg := loadWithSecrets(nil)
	if cfg.GeoIPSource != "" || !strings.HasSuffix(cfg.GeoIPCacheDir, filepath.Join("lumen", "geoip")) {
		t.Fatalf("defaults: source=%q dir=%q", cfg.GeoIPSource, cfg.GeoIPCacheDir)
	}
	t.Setenv("GEOIP_SOURCE", "dbip-lite")
	t.Setenv("GEOIP_CACHE_DIR", "/data/geo")
	cfg = loadWithSecrets(map[string]string{"GEOIP_CACHE_DIR": "/secret/geo"})
	if cfg.GeoIPSource != "dbip-lite" || cfg.GeoIPCacheDir != "/secret/geo" {
		t.Fatalf("override: source=%q dir=%q", cfg.GeoIPSource, cfg.GeoIPCacheDir)
	}
	cfg.DevMode = true
	cfg.GeoIPSource = "maxmind"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsupported GEOIP_SOURCE accepted")
	}
}
