package ch

import (
	"context"
	"strings"
)

// databaseSQL rewrites database identifiers in internal SQL templates. It leaves
// string literals, quoted identifiers and comments unchanged.
func (c *Client) databaseSQL(query string) string {
	if c.database == "" || c.database == "lumen" {
		return query
	}
	return transformSQL(query, c.database, false)
}
func stripSQLComments(query string) string { return transformSQL(query, "", true) }
func transformSQL(query, database string, stripComments bool) string {
	var out strings.Builder
	for i := 0; i < len(query); {
		start := i
		if strings.HasPrefix(query[i:], "--") || query[i] == '#' {
			for i < len(query) && query[i] != '\n' {
				i++
			}
			if stripComments {
				out.WriteByte(' ')
			} else {
				out.WriteString(query[start:i])
			}
			continue
		}
		if strings.HasPrefix(query[i:], "/*") {
			i += 2
			for i < len(query) && !strings.HasPrefix(query[i:], "*/") {
				i++
			}
			if i < len(query) {
				i += 2
			}
			if stripComments {
				out.WriteByte(' ')
			} else {
				out.WriteString(query[start:i])
			}
			continue
		}
		if query[i] == '\'' || query[i] == '"' || query[i] == '`' {
			quote := query[i]
			i++
			for i < len(query) {
				if query[i] == '\\' {
					i++
					if i < len(query) {
						i++
					}
					continue
				}
				if query[i] == quote {
					i++
					if i < len(query) && query[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(query[start:i])
			continue
		}
		if isSQLWord(query[i]) {
			for i < len(query) && isSQLWord(query[i]) {
				i++
			}
			word := query[start:i]
			if database != "" && word == "lumen" && i < len(query) && query[i] == '.' {
				word = database
			}
			out.WriteString(word)
			continue
		}
		out.WriteByte(query[i])
		i++
	}
	return out.String()
}
func isSQLWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}
func (c *Client) exec(ctx context.Context, query string, args ...any) error {
	return c.conn.Exec(ctx, c.databaseSQL(query), args...)
}

func (c *Client) DatabaseName() string {
	if c.database == "" {
		return "lumen"
	}
	return c.database
}
func (c *Client) NativePort() int {
	if c.port == 0 {
		return 9000
	}
	return c.port
}
