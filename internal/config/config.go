// Package config provides environmental configuration loading for the Lumen service.
package config

import (
	"os"
	"strconv"
)

// Config holds runtime configuration settings for Lumen.
type Config struct {
	IngestPort    int    // Port for public telemetry ingest (Connect + gRPC)
	AdminPort     int    // Port for internal control plane RPCs
	MetricsPort   int    // Port for Prometheus metrics and health endpoints
	AdminToken    string // Internal authorization token for Admin RPCs
	ClickHouseDSN string // Connection DSN for ClickHouse cluster
	PostgresDSN   string // Connection DSN for Postgres control plane DB
	GeoIPDBPath   string // Optional file path to MaxMind GeoLite2-City.mmdb
}

// Load loads configuration parameters from environment variables or Infisical directly into memory.
func Load() *Config {
	cfg := &Config{
		IngestPort:    getEnvAsInt("INGEST_PORT", 50051),
		AdminPort:     getEnvAsInt("ADMIN_PORT", 50052),
		MetricsPort:   getEnvAsInt("METRICS_PORT", 9090),
		AdminToken:    getEnv("ADMIN_TOKEN", "lumen-internal-secret-token"),
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
