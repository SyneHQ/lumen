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
	conn     driver.Conn
	database string
	host     string
	port     int
}

// NewClient initializes a native ClickHouse connection pool using DSN parameters.
func NewClient(dsn string) (*Client, error) {
	return NewClientContext(context.Background(), dsn)
}

// NewClientContext bounds connection setup by the caller deadline.
func NewClientContext(parent context.Context, dsn string) (*Client, error) {
	return NewClientContextWithOptions(parent, dsn, ClientOptions{})
}

// NewClientContextWithOptions applies explicit transport options before connecting.
// Empty options preserve the connection settings in the DSN.
func NewClientContextWithOptions(parent context.Context, dsn string, options ClientOptions) (*Client, error) {
	opts, err := parseClientOptions(dsn, options)
	if err != nil {
		return nil, err
	}

	database := opts.Auth.Database
	if database == "" || database == "default" {
		database = "lumen"
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`).MatchString(database) {
		return nil, fmt.Errorf("ClickHouse database name is invalid")
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to clickhouse: %w", err)
	}

	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse ping failed: %w", err)
	}

	host, port := nativeEndpoint(opts)
	return &Client{conn: conn, database: database, host: host, port: port}, nil
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
		if c.database != "" && c.database != "lumen" && strings.TrimSpace(stripSQLComments(stmt)) == "CREATE DATABASE IF NOT EXISTS lumen" {
			continue
		}
		if err := c.exec(ctx, stmt); err != nil {
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

	batch, err := c.conn.PrepareBatch(asyncCtx, c.databaseSQL(`
		INSERT INTO lumen.events (
			team_id, ts, name, event_id, anon_id, user_id, session_id,
			sdk, sdk_version, app_version, os, os_version, device_type, device_model, manufacturer,
			browser, browser_version, screen_w, screen_h, viewport_w, viewport_h, locale, timezone,
			url, path, host, referrer, referrer_host, utm_source, utm_medium, utm_campaign, utm_term, utm_content,
			country, region, city, ip, props
		)
	`))
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
	return c.exec(ctx, query, teamID, anonID, userID)
}

// ProvisionTenant creates ClickHouse user, table row security policies, and quotas for a team (§4).
func (c *Client) ProvisionTenant(ctx context.Context, teamID, chUser, password string) error {
	if err := c.CreateTenantUser(ctx, chUser, password); err != nil {
		return err
	}
	return c.EnsureTenantAccess(ctx, teamID, chUser)
}

// CreateTenantUser must succeed before the caller persists user ownership.
// Duplicate-user and transport errors do not establish ownership.
func (c *Client) CreateTenantUser(ctx context.Context, chUser, password string) error {
	if err := validateTenantUser(chUser); err != nil {
		return err
	}
	query := fmt.Sprintf("CREATE USER %s IDENTIFIED WITH sha256_password BY ? SETTINGS max_execution_time = 60, max_memory_usage = 4000000000 READONLY", chUser)
	return c.exec(ctx, query, password)
}

func validateTenantUser(user string) error {
	if !regexp.MustCompile(`^lumen_t_[A-Za-z0-9_]+$`).MatchString(user) {
		return fmt.Errorf("tenant database username is invalid")
	}
	return nil
}

// EnsureTenantAccess idempotently (re)applies SELECT grants, row security
// policies, and the hourly quota for an existing tenant user. It is called on
// every boot so tenants provisioned before new tables/views shipped pick up
// access automatically without re-provisioning.
func (c *Client) EnsureTenantAccess(ctx context.Context, teamID, chUser string) error {
	if err := validateTenantUser(chUser); err != nil {
		return err
	}
	slug := sanitizeSlug(teamID)

	// Create per-table row security policies
	policies := []string{
		fmt.Sprintf("CREATE ROW POLICY IF NOT EXISTS pol_ev_%s ON lumen.events USING team_id = ? TO %s", slug, chUser),
		fmt.Sprintf("CREATE ROW POLICY IF NOT EXISTS pol_sess_%s ON lumen.sessions USING team_id = ? TO %s", slug, chUser),
		fmt.Sprintf("CREATE ROW POLICY IF NOT EXISTS pol_ident_%s ON lumen.identities USING team_id = ? TO %s", slug, chUser),
		fmt.Sprintf("CREATE ROW POLICY IF NOT EXISTS pol_pers_%s ON lumen.persons USING team_id = ? TO %s", slug, chUser),
	}
	for _, p := range policies {
		if err := c.exec(ctx, p, teamID); err != nil {
			return fmt.Errorf("failed to create row policy (%s): %w", p, err)
		}
	}

	// Create resource quota
	quotaDDL := fmt.Sprintf(
		"CREATE QUOTA IF NOT EXISTS q_%s FOR INTERVAL 1 hour MAX queries = 1000, result_rows = 100000000 TO %s",
		slug, chUser,
	)
	if err := c.exec(ctx, quotaDDL); err != nil {
		return fmt.Errorf("failed to create quota: %w", err)
	}

	// Grant table & view SELECT access
	grants := []string{
		fmt.Sprintf("GRANT SELECT ON lumen.events TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.events_resolved TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.sessions TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.sessions_v TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.identities TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.identities_v TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.persons TO %s", chUser),
		fmt.Sprintf("GRANT SELECT ON lumen.persons_v TO %s", chUser),
	}
	for _, g := range grants {
		if err := c.exec(ctx, g); err != nil {
			return fmt.Errorf("failed to grant permission (%s): %w", g, err)
		}
	}

	return nil
}

// DeprovisionTenant revokes and removes ClickHouse user credentials, policies, and quotas.
func (c *Client) DeprovisionTenant(ctx context.Context, teamID, chUser string) error {
	return removeTenantAccess(ctx, c.exec, teamID, chUser)
}

func removeTenantAccess(ctx context.Context, exec func(context.Context, string, ...any) error, teamID, user string) error {
	if err := validateTenantUser(user); err != nil {
		return err
	}
	slug := sanitizeSlug(teamID)
	// Remove the principal before removing its row restrictions. Stop on failure.
	statements := []string{
		fmt.Sprintf("DROP USER IF EXISTS %s", user),
		fmt.Sprintf("DROP ROW POLICY IF EXISTS pol_ev_%s ON lumen.events", slug),
		fmt.Sprintf("DROP ROW POLICY IF EXISTS pol_sess_%s ON lumen.sessions", slug),
		fmt.Sprintf("DROP ROW POLICY IF EXISTS pol_ident_%s ON lumen.identities", slug),
		fmt.Sprintf("DROP ROW POLICY IF EXISTS pol_pers_%s ON lumen.persons", slug),
		fmt.Sprintf("DROP QUOTA IF EXISTS q_%s", slug),
	}
	for _, statement := range statements {
		if err := exec(ctx, statement); err != nil {
			return fmt.Errorf("remove tenant access: %w", err)
		}
	}
	return nil
}

// DeleteUserData executes a GDPR user data deletion query. Mutations are
// submitted for both the raw events stream and the derived persons rollup so
// extracted traits (email/name/…) are erased too.
func (c *Client) DeleteUserData(ctx context.Context, teamID, userID, anonID string) error {
	eventsQuery := "ALTER TABLE lumen.events DELETE WHERE team_id = ? AND (user_id = ? OR anon_id = ?)"
	if err := c.exec(ctx, eventsQuery, teamID, userID, anonID); err != nil {
		return err
	}

	// person_id is the user_id once known and the anon_id before that.
	personsQuery := "ALTER TABLE lumen.persons DELETE WHERE team_id = ? AND (person_id = ? OR person_id = ?)"
	return c.exec(ctx, personsQuery, teamID, userID, anonID)
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
