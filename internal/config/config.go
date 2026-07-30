// Package config provides environmental configuration loading for the Lumen service.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
)

// minAdminTokenLen is the shortest ADMIN_TOKEN we will accept. The admin port
// can provision tenants and mint API keys, so a guessable token is a full
// compromise of every tenant in the deployment.
const minAdminTokenLen = 32

// knownWeakAdminTokens are values that shipped as defaults or appeared in
// examples and documentation. They must never be accepted, at any length.
var knownWeakAdminTokens = map[string]bool{
	"lumen-internal-secret-token": true,
	"test-admin-secret-token":     true,
	"changeme":                    true,
	"secret":                      true,
	"admin":                       true,
}

// Config holds runtime configuration settings for Lumen.
type Config struct {
	IngestPort    int    // Port for public telemetry ingest (Connect + gRPC)
	AdminPort     int    // Port for internal control plane RPCs
	MetricsPort   int    // Port for Prometheus metrics and health endpoints
	AdminToken    string // Internal authorization token for Admin RPCs
	ClickHouseDSN string // Connection DSN for ClickHouse cluster
	PostgresDSN   string // Connection DSN for Postgres control plane DB
	GeoIPDBPath   string // Optional file path to MaxMind GeoLite2-City.mmdb
	DevMode       bool   // When true, generate an ephemeral admin token instead of failing
}

// Load loads configuration parameters from environment variables or Infisical directly into memory.
//
// Note that ADMIN_TOKEN deliberately has no default value. An insecure fallback
// would silently expose tenant provisioning on every deployment that forgot to
// set it. Callers must run Validate, which rejects an empty or weak token unless
// LUMEN_DEV is set.
func Load() *Config {
	cfg := &Config{
		IngestPort:    getEnvAsInt("INGEST_PORT", 50051),
		AdminPort:     getEnvAsInt("ADMIN_PORT", 50052),
		MetricsPort:   getEnvAsInt("METRICS_PORT", 9090),
		AdminToken:    getEnv("ADMIN_TOKEN", ""),
		DevMode:       getEnvAsBool("LUMEN_DEV", false),
		ClickHouseDSN: getEnv("CLICKHOUSE_DSN", "clickhouse://127.0.0.1:9000/lumen?dial_timeout=10s&compress=true"),
		PostgresDSN:   getEnv("POSTGRES_DSN", "postgres://postgres:postgres@localhost:5432/lumen?sslmode=disable"),
		GeoIPDBPath:   getEnv("GEOIP_DB_PATH", ""),
	}

	// Apply secrets directly from Infisical in memory without exposing to process environment
	secrets := FetchInfisicalSecrets()
	if secrets != nil {
		if val, ok := secrets["INGEST_PORT"]; ok && val != "" {
			if port, err := strconv.Atoi(val); err == nil {
				cfg.IngestPort = port
			}
		}
		if val, ok := secrets["ADMIN_PORT"]; ok && val != "" {
			if port, err := strconv.Atoi(val); err == nil {
				cfg.AdminPort = port
			}
		}
		if val, ok := secrets["METRICS_PORT"]; ok && val != "" {
			if port, err := strconv.Atoi(val); err == nil {
				cfg.MetricsPort = port
			}
		}
		if val, ok := secrets["ADMIN_TOKEN"]; ok && val != "" {
			cfg.AdminToken = val
		}
		if val, ok := secrets["CLICKHOUSE_DSN"]; ok && val != "" {
			cfg.ClickHouseDSN = val
		}
		if val, ok := secrets["POSTGRES_DSN"]; ok && val != "" {
			cfg.PostgresDSN = val
		}
		if val, ok := secrets["GEOIP_DB_PATH"]; ok && val != "" {
			cfg.GeoIPDBPath = val
		}
	}

	return cfg
}

// Validate checks that the configuration is safe to run with, and fills in
// development-only values when LUMEN_DEV is set. Callers must treat a returned
// error as fatal.
func (c *Config) Validate() error {
	if c.AdminToken == "" {
		if !c.DevMode {
			return errors.New(
				"ADMIN_TOKEN is not set: the admin port provisions tenants and mints API keys, " +
					"so it has no default. Generate one with `openssl rand -hex 32` and set ADMIN_TOKEN, " +
					"or set LUMEN_DEV=1 for a throwaway local token")
		}

		token, err := generateToken(minAdminTokenLen)
		if err != nil {
			return fmt.Errorf("generate ephemeral dev admin token: %w", err)
		}
		c.AdminToken = token
		log.Printf("LUMEN_DEV: generated ephemeral ADMIN_TOKEN=%s (development only, changes every restart)", token)
		return c.validatePorts()
	}

	if knownWeakAdminTokens[c.AdminToken] {
		return fmt.Errorf(
			"ADMIN_TOKEN is set to the well-known value %q, which is published in Lumen's own docs and git history; "+
				"generate a real one with `openssl rand -hex 32`", c.AdminToken)
	}

	if len(c.AdminToken) < minAdminTokenLen {
		return fmt.Errorf("ADMIN_TOKEN must be at least %d characters, got %d", minAdminTokenLen, len(c.AdminToken))
	}

	if c.ClickHouseDSN == "" {
		return errors.New("CLICKHOUSE_DSN must not be empty")
	}
	if c.PostgresDSN == "" {
		return errors.New("POSTGRES_DSN must not be empty")
	}

	return c.validatePorts()
}

func (c *Config) validatePorts() error {
	ports := map[string]int{
		"INGEST_PORT":  c.IngestPort,
		"ADMIN_PORT":   c.AdminPort,
		"METRICS_PORT": c.MetricsPort,
	}
	seen := make(map[int]string, len(ports))
	for name, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s must be between 1 and 65535, got %d", name, port)
		}
		if other, dup := seen[port]; dup {
			return fmt.Errorf("%s and %s are both set to port %d", other, name, port)
		}
		seen[port] = name
	}
	return nil
}

// generateToken returns a cryptographically random hex string of n characters.
func generateToken(n int) (string, error) {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf)[:n], nil
}

// Helper function to read string environment variable with fallback default.
func getEnv(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

// Helper function to read integer environment variable with fallback default.
func getEnvAsInt(key string, defaultValue int) int {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return val
		}
	}
	return defaultValue
}

// Helper function to read boolean environment variable with fallback default.
func getEnvAsBool(key string, defaultValue bool) bool {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.ParseBool(valStr); err == nil {
			return val
		}
	}
	return defaultValue
}
