package ch

import (
	"errors"
	"net"
	"strconv"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// ClientOptions contains explicit overrides for DSN connection settings.
type ClientOptions struct {
	// Compression accepts empty or lz4. Empty preserves the DSN compression settings.
	// lz4 replaces the DSN method and level with LZ4 at the driver default level.
	Compression string
}

func parseClientOptions(dsn string, overrides ClientOptions) (*clickhouse.Options, error) {
	if overrides.Compression != "" && overrides.Compression != "lz4" {
		return nil, errors.New("ClickHouse compression override must be empty or lz4")
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		// Driver parse errors can contain the credential-bearing DSN.
		return nil, errors.New("CLICKHOUSE_DSN is invalid. Check the connection settings")
	}
	if overrides.Compression == "lz4" {
		opts.Compression = &clickhouse.Compression{Method: clickhouse.CompressionLZ4}
	}
	return opts, nil
}

// nativeEndpoint advertises the first driver address, as NativePort has always done.
// Driver options remain unchanged, including TLS and protocol selection.
func nativeEndpoint(opts *clickhouse.Options) (string, int) {
	host, port := "localhost", 9000
	if len(opts.Addr) > 0 {
		if h, p, err := net.SplitHostPort(opts.Addr[0]); err == nil {
			host = h
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
	}
	return host, port
}
