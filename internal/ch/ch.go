// Package ch manages ClickHouse database connection pools, DDL migrations, and async batch ingestion.
package ch

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
)

// EventRecord maps Go types directly to the lumen.events ClickHouse table schema.
type EventRecord struct {
	TeamID         string
	TS             time.Time
	Name           string
	EventID        uuid.UUID
	AnonID         string
	UserID         string
	SessionID      string
	SDK            string
	SDKVersion     string
	AppVersion     string
	OS             string
	OSVersion      string
	DeviceType     string
	DeviceModel    string
	Manufacturer   string
	Browser        string
	BrowserVersion string
	ScreenW        uint16
	ScreenH        uint16
	ViewportW      uint16
	ViewportH      uint16
	Locale         string
	Timezone       string
	URL            string
	Path           string
	Host           string
	Referrer       string
	ReferrerHost   string
	UTMSource      string
	UTMMedium      string
	UTMCampaign    string
	UTMTerm        string
	UTMContent     string
	Country        string
	Region         string
	City           string
	IP             net.IP
	Props          string // Raw JSON string
}

// Client wraps the native clickhouse-go driver connection pool.
type Client struct {
	conn driver.Conn
}

// NewClient initializes a native ClickHouse connection pool using DSN parameters.
func NewClient(dsn string) (*Client, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse clickhouse dsn: %w", err)
	}

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to clickhouse: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("clickhouse ping failed: %w", err)
	}

	return &Client{conn: conn}, nil
}

// Close closes the underlying ClickHouse connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// RunMigrations executes DDL migration statements against ClickHouse.
func (c *Client) RunMigrations(ctx context.Context, migrationSQL string) error {
	statements := strings.Split(migrationSQL, ";")
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := c.conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("failed to execute clickhouse statement (%s...): %w", truncate(stmt, 50), err)
		}
	}
	return nil
}

// InsertBatch inserts a batch of events with server-side buffering (async_insert = 1).
func (c *Client) InsertBatch(ctx context.Context, events []EventRecord, dedupToken string) error {
	if len(events) == 0 {
		return nil
	}

	// Apply async insert and durability settings (§5.4)
	asyncCtx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":               1,
		"wait_for_async_insert":      1,
		"insert_deduplication_token": dedupToken,
	}))

	batch, err := c.conn.PrepareBatch(asyncCtx, `
		INSERT INTO lumen.events (
			team_id, ts, name, event_id, anon_id, user_id, session_id,
			sdk, sdk_version, app_version, os, os_version, device_type, device_model, manufacturer,
			browser, browser_version, screen_w, screen_h, viewport_w, viewport_h, locale, timezone,
			url, path, host, referrer, referrer_host, utm_source, utm_medium, utm_campaign, utm_term, utm_content,
			country, region, city, ip, props
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	for _, e := range events {
		err := batch.Append(
			e.TeamID, e.TS, e.Name, e.EventID, e.AnonID, e.UserID, e.SessionID,
			e.SDK, e.SDKVersion, e.AppVersion, e.OS, e.OSVersion, e.DeviceType, e.DeviceModel, e.Manufacturer,
			e.Browser, e.BrowserVersion, e.ScreenW, e.ScreenH, e.ViewportW, e.ViewportH, e.Locale, e.Timezone,
			e.URL, e.Path, e.Host, e.Referrer, e.ReferrerHost, e.UTMSource, e.UTMMedium, e.UTMCampaign, e.UTMTerm, e.UTMContent,
			e.Country, e.Region, e.City, e.IP, e.Props,
		)
		if err != nil {
			return fmt.Errorf("failed to append row to batch: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("failed to send clickhouse batch: %w", err)
	}

	return nil
}

// InsertIdentity records an identity mapping (anon_id -> user_id) in ClickHouse.
func (c *Client) InsertIdentity(ctx context.Context, teamID, anonID, userID string) error {
	query := `
		INSERT INTO lumen.identities (team_id, anon_id, user_id)
		VALUES (?, ?, ?)
	`
	return c.conn.Exec(ctx, query, teamID, anonID, userID)
}

// ProvisionTenant creates ClickHouse user, table row security policies, and quotas for a team (§4).
func (c *Client) ProvisionTenant(ctx context.Context, teamID, chUser, password string) error {
	slug := sanitizeSlug(teamID)

	// 1. Create ClickHouse User with READONLY restriction
	createUserDDL := fmt.Sprintf(
		"CREATE USER IF NOT EXISTS %s IDENTIFIED WITH sha256_password BY '%s' SETTINGS max_execution_time = 60, max_memory_usage = 4000000000 READONLY",
		chUser, password,
	)
	if err := c.conn.Exec(ctx, createUserDDL); err != nil {
		return fmt.Errorf("failed to create clickhouse user: %w", err)
	}

	// 2. Grant table & view SELECT access
	grants := []string{
		fmt.Sprintf("GRANT SELECT ON lumen.events TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.events_resolved TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.sessions TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.sessions_v TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.identities TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.identities_v TO %s", chUser),
	}
	for _, g := range grants {
		if err := c.conn.Exec(ctx, g); err != nil {
			return fmt.Errorf("failed to grant permission (%s): %w", g, err)
		}
	}

	// 3. Create per-table row security policies
	policies := []string{
		fmt.Sprintf("CREATE ROW POLICY IF NOT EXISTS pol_ev_%s ON lumen.events USING team_id = '%s' TO %s", slug, teamID, chUser),
		fmt.Sprintf("CREATE ROW POLICY IF NOT EXISTS pol_sess_%s ON lumen.sessions USING team_id = '%s' TO %s", slug, teamID, chUser),
		fmt.Sprintf("CREATE ROW POLICY IF NOT EXISTS pol_ident_%s ON lumen.identities USING team_id = '%s' TO %s", slug, teamID, chUser),
	}
	for _, p := range policies {
		if err := c.conn.Exec(ctx, p); err != nil {
			return fmt.Errorf("failed to create row policy (%s): %w", p, err)
		}
	}

	// 4. Create resource quota
	quotaDDL := fmt.Sprintf(
		"CREATE QUOTA IF NOT EXISTS q_%s FOR INTERVAL 1 hour MAX queries = 1000, result_rows = 100000000 TO %s",
		slug, chUser,
	)
	if err := c.conn.Exec(ctx, quotaDDL); err != nil {
		return fmt.Errorf("failed to create quota: %w", err)
	}

	return nil
}

// DeprovisionTenant revokes and removes ClickHouse user credentials, policies, and quotas.
func (c *Client) DeprovisionTenant(ctx context.Context, teamID, chUser string) error {
	slug := sanitizeSlug(teamID)

	_ = c.conn.Exec(ctx, fmt.Sprintf("DROP ROW POLICY IF EXISTS pol_ev_%s ON lumen.events", slug))
	_ = c.conn.Exec(ctx, fmt.Sprintf("DROP ROW POLICY IF EXISTS pol_sess_%s ON lumen.sessions", slug))
	_ = c.conn.Exec(ctx, fmt.Sprintf("DROP ROW POLICY IF EXISTS pol_ident_%s ON lumen.identities", slug))
	_ = c.conn.Exec(ctx, fmt.Sprintf("DROP QUOTA IF EXISTS q_%s", slug))
	_ = c.conn.Exec(ctx, fmt.Sprintf("DROP USER IF EXISTS %s", chUser))

	return nil
}

// DeleteUserData executes a GDPR user data deletion query.
func (c *Client) DeleteUserData(ctx context.Context, teamID, userID, anonID string) error {
	query := "ALTER TABLE lumen.events DELETE WHERE team_id = ? AND (user_id = ? OR anon_id = ?)"
	return c.conn.Exec(ctx, query, teamID, userID, anonID)
}

func sanitizeSlug(input string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	return reg.ReplaceAllString(input, "_")
}

func truncate(s string, l int) string {
	if len(s) <= l {
		return s
	}
	return s[:l] + "..."
}
