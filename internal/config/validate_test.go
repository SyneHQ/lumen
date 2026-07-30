package config

import (
	"strings"
	"testing"
)

func baseConfig() *Config {
	return &Config{
		IngestPort:    50051,
		AdminPort:     50052,
		MetricsPort:   9090,
		AdminToken:    strings.Repeat("a", minAdminTokenLen),
		ClickHouseDSN: "clickhouse://127.0.0.1:9000/lumen",
		PostgresDSN:   "postgres://postgres:postgres@localhost:5432/lumen?sslmode=disable",
	}
}

func TestValidateRejectsEmptyAdminToken(t *testing.T) {
	cfg := baseConfig()
	cfg.AdminToken = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for empty ADMIN_TOKEN, got nil")
	}
	if !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Errorf("error should name ADMIN_TOKEN, got: %v", err)
	}
}

func TestValidateRejectsKnownWeakTokens(t *testing.T) {
	// The value that used to ship as the default must never be accepted again.
	for token := range knownWeakAdminTokens {
		cfg := baseConfig()
		cfg.AdminToken = token

		if err := cfg.Validate(); err == nil {
			t.Errorf("expected known-weak token %q to be rejected", token)
		}
	}
}

func TestValidateRejectsShortToken(t *testing.T) {
	cfg := baseConfig()
	cfg.AdminToken = strings.Repeat("a", minAdminTokenLen-1)

	if err := cfg.Validate(); err == nil {
		t.Errorf("expected token shorter than %d chars to be rejected", minAdminTokenLen)
	}
}

func TestValidateAcceptsStrongToken(t *testing.T) {
	cfg := baseConfig()

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected a strong token to pass, got: %v", err)
	}
}

func TestValidateDevModeGeneratesEphemeralToken(t *testing.T) {
	cfg := baseConfig()
	cfg.AdminToken = ""
	cfg.DevMode = true

	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev mode should generate a token, got: %v", err)
	}
	if len(cfg.AdminToken) != minAdminTokenLen {
		t.Errorf("expected generated token of length %d, got %d", minAdminTokenLen, len(cfg.AdminToken))
	}
	if knownWeakAdminTokens[cfg.AdminToken] {
		t.Error("generated token must not be a known weak value")
	}

	// Two dev-mode runs must not produce the same token.
	other := baseConfig()
	other.AdminToken = ""
	other.DevMode = true
	if err := other.Validate(); err != nil {
		t.Fatalf("second dev-mode validate failed: %v", err)
	}
	if other.AdminToken == cfg.AdminToken {
		t.Error("ephemeral dev tokens must differ between runs")
	}
}

func TestValidateRejectsDuplicatePorts(t *testing.T) {
	cfg := baseConfig()
	cfg.AdminPort = cfg.IngestPort

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error when two services share a port, got nil")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("error should mention the port conflict, got: %v", err)
	}
}

func TestValidateRejectsOutOfRangePorts(t *testing.T) {
	for _, port := range []int{0, -1, 65536} {
		cfg := baseConfig()
		cfg.IngestPort = port

		if err := cfg.Validate(); err == nil {
			t.Errorf("expected port %d to be rejected", port)
		}
	}
}

func TestValidateRejectsEmptyDSNs(t *testing.T) {
	cfg := baseConfig()
	cfg.ClickHouseDSN = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected empty CLICKHOUSE_DSN to be rejected")
	}

	cfg = baseConfig()
	cfg.PostgresDSN = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected empty POSTGRES_DSN to be rejected")
	}
}

func TestGenerateTokenLength(t *testing.T) {
	for _, n := range []int{16, 32, 33, 64} {
		tok, err := generateToken(n)
		if err != nil {
			t.Fatalf("generateToken(%d): %v", n, err)
		}
		if len(tok) != n {
			t.Errorf("generateToken(%d) returned length %d", n, len(tok))
		}
	}
}
