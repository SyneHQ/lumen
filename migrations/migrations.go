// Package migrations embeds ClickHouse and Postgres DDL migration files.
package migrations

import "embed"

// FS embeds all .sql migration files for ClickHouse and Postgres.
//
//go:embed ch/*.sql pg/*.sql
var FS embed.FS
