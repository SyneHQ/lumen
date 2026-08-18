// Package pg manages Postgres interactions for the Lumen control plane.
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// APIKeyRecord represents a stored API key entry in Postgres.
type APIKeyRecord struct {
	KeyPrefix string
	Name      string
	TeamID    string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// TenantRecord represents a provisioned tenant row in lumen_tenants.
type TenantRecord struct {
	TeamID  string
	CHUser  string
	StoreIP bool
}

// Store provides thread-safe access to Postgres control plane metadata.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore initializes a Postgres connection pool using the provided DSN string.
func NewStore(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse postgres dsn: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}

	// Ping database to verify connection health
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close closes the underlying Postgres connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// RunMigrations executes embedded DDL statements to set up control plane tables.
func (s *Store) RunMigrations(ctx context.Context, migrationSQL string) error {
	_, err := s.pool.Exec(ctx, migrationSQL)
	if err != nil {
		return fmt.Errorf("failed to execute postgres migration: %w", err)
	}
	return nil
}

// GetTeamIDByKeyHash resolves an active ingest API key hash to its team ID and privacy preferences.
func (s *Store) GetTeamIDByKeyHash(ctx context.Context, keyHash []byte) (string, bool, error) {
	query := `
		SELECT k.team_id, t.store_ip
		FROM lumen_api_keys k
		JOIN lumen_tenants t ON k.team_id = t.team_id
		WHERE k.key_hash = $1 AND k.revoked_at IS NULL
	`
	var teamID string
	var storeIP bool

	err := s.pool.QueryRow(ctx, query, keyHash).Scan(&teamID, &storeIP)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", false, fmt.Errorf("invalid or revoked api key")
		}
		return "", false, err
	}

	return teamID, storeIP, nil
}

// RegisterTenant inserts a new tenant row in lumen_tenants.
func (s *Store) RegisterTenant(ctx context.Context, teamID, chUser string, storeIP bool) error {
	query := `
		INSERT INTO lumen_tenants (team_id, ch_user, store_ip)
		VALUES ($1, $2, $3)
		ON CONFLICT (team_id) DO UPDATE SET store_ip = EXCLUDED.store_ip
	`
	_, err := s.pool.Exec(ctx, query, teamID, chUser, storeIP)
	if err != nil {
		return fmt.Errorf("failed to register tenant: %w", err)
	}
	return nil
}

// ListTenants returns all tenants registered in the control plane. Used at
// boot to reconcile ClickHouse grants/policies for tenants provisioned before
// new tables or views shipped.
func (s *Store) ListTenants(ctx context.Context) ([]TenantRecord, error) {
	query := `SELECT team_id, ch_user, store_ip FROM lumen_tenants ORDER BY created_at`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []TenantRecord
	for rows.Next() {
		var rec TenantRecord
		if err := rows.Scan(&rec.TeamID, &rec.CHUser, &rec.StoreIP); err != nil {
			return nil, err
		}
		tenants = append(tenants, rec)
	}
	return tenants, nil
}

// SaveAPIKey records a newly generated API key hash in lumen_api_keys.
func (s *Store) SaveAPIKey(ctx context.Context, keyHash []byte, keyPrefix, name, teamID string) error {
	if name == "" {
		name = "Default Ingestion Key"
	}
	query := `
		INSERT INTO lumen_api_keys (key_hash, key_prefix, name, team_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (key_hash) DO NOTHING
	`
	_, err := s.pool.Exec(ctx, query, keyHash, keyPrefix, name, teamID)
	if err != nil {
		return fmt.Errorf("failed to save api key: %w", err)
	}
	return nil
}

// RevokeSpecificKey decommissions (revokes) a single API key for a team by prefix.
func (s *Store) RevokeSpecificKey(ctx context.Context, teamID, keyPrefix string) error {
	query := `
		UPDATE lumen_api_keys
		SET revoked_at = $1
		WHERE team_id = $2 AND (key_prefix = $3 OR key_prefix LIKE $4) AND revoked_at IS NULL
	`
	prefixPattern := keyPrefix + "%"
	_, err := s.pool.Exec(ctx, query, time.Now(), teamID, keyPrefix, prefixPattern)
	if err != nil {
		return fmt.Errorf("failed to revoke key: %w", err)
	}
	return nil
}

// RevokeTeamKeys marks ALL API keys for a specific team as revoked.
func (s *Store) RevokeTeamKeys(ctx context.Context, teamID string) error {
	query := `
		UPDATE lumen_api_keys
		SET revoked_at = $1
		WHERE team_id = $2 AND revoked_at IS NULL
	`
	_, err := s.pool.Exec(ctx, query, time.Now(), teamID)
	if err != nil {
		return fmt.Errorf("failed to revoke team api keys: %w", err)
	}
	return nil
}

// ListTeamKeys fetches metadata for all keys provisioned for a team.
func (s *Store) ListTeamKeys(ctx context.Context, teamID string) ([]APIKeyRecord, error) {
	query := `
		SELECT key_prefix, name, team_id, created_at, revoked_at
		FROM lumen_api_keys
		WHERE team_id = $1
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, query, teamID)
	if err != nil {
		return nil, fmt.Errorf("failed to query team keys: %w", err)
	}
	defer rows.Close()

	var keys []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		if err := rows.Scan(&rec.KeyPrefix, &rec.Name, &rec.TeamID, &rec.CreatedAt, &rec.RevokedAt); err != nil {
			return nil, err
		}
		keys = append(keys, rec)
	}
	return keys, nil
}
