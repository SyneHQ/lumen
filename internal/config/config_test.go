package config

import (
	"os"
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
