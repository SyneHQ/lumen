package ch

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestClientOptionsPreserveDSNWhenCompressionIsUnset(t *testing.T) {
	for _, query := range []string{"", "?compress=false", "?compress=true", "?compress=zstd&compress_level=4", "?compress=lz4&compress_level=3"} {
		t.Run(query, func(t *testing.T) {
			dsn := "clickhouse://test-user:test-password@localhost:9440/app" + query
			expected, err := clickhouse.ParseDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := parseClientOptions(dsn, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("empty override changed DSN options")
			}
		})
	}
}

func TestClientOptionsLZ4OverridesOnlyCompression(t *testing.T) {
	for _, query := range []string{"", "&compress=false", "&compress=zstd&compress_level=7", "&compress=lz4&compress_level=3"} {
		t.Run(query, func(t *testing.T) {
			dsn := "clickhouse://test-user:test-password@database.internal:9440/app?secure=true&skip_verify=false&tls_server_name=database.internal&dial_timeout=8s&read_timeout=17s&max_open_conns=9&max_idle_conns=3&block_buffer_size=4&max_execution_time=10" + query
			before, err := clickhouse.ParseDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			opts, err := parseClientOptions(dsn, ClientOptions{Compression: "lz4"})
			if err != nil {
				t.Fatal(err)
			}
			if opts.Compression == nil || opts.Compression.Method != clickhouse.CompressionLZ4 || opts.Compression.Level != 0 {
				t.Fatal("LZ4 did not replace the DSN compression method and level")
			}
			if opts.TLS == nil || opts.TLS.InsecureSkipVerify || opts.TLS.ServerName != "database.internal" {
				t.Fatal("compression changed verified TLS")
			}
			if opts.Auth.Database != "app" || opts.Auth.Username != "test-user" || opts.Auth.Password != "test-password" || opts.DialTimeout != 8*time.Second || opts.ReadTimeout != 17*time.Second {
				t.Fatal("compression changed connection settings")
			}
			before.Compression = opts.Compression
			if !reflect.DeepEqual(opts, before) {
				t.Fatal("compression changed unrelated driver options")
			}
		})
	}
}

func TestClientOptionsRejectInvalidOverridesBeforeConnecting(t *testing.T) {
	for _, value := range []string{"zstd", "none", "LZ4", " lz4"} {
		if _, err := parseClientOptions("clickhouse://127.0.0.1:1/app", ClientOptions{Compression: value}); err == nil {
			t.Fatal("accepted unsupported override")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := NewClientContextWithOptions(ctx, "clickhouse://127.0.0.1:1/app", ClientOptions{Compression: "unsupported"}); err == nil || !strings.Contains(err.Error(), "compression override") {
		t.Fatal("invalid override reached the connection path")
	}
}

func TestClientOptionsDoNotExposeInvalidDSNCredentials(t *testing.T) {
	const password = "private-password-marker"
	for _, dsn := range []string{
		"clickhouse://user:" + password + "@bad host/app",
		"clickhouse://user:" + password + "@host/app?dial_timeout=" + password,
	} {
		_, err := parseClientOptions(dsn, ClientOptions{Compression: "lz4"})
		if err == nil {
			t.Fatal("accepted invalid DSN")
		}
		if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), dsn) {
			t.Fatal("parse error exposed connection credentials")
		}
	}
}

func TestNativeEndpointUsesFirstDriverAddress(t *testing.T) {
	for _, tc := range []struct {
		name, dsn, host string
		port            int
	}{
		{"dns", "clickhouse://user:private-marker@database.internal:9000/app", "database.internal", 9000},
		{"verified_tls", "clickhouse://user:private-marker@database.internal:9440/app?secure=true&skip_verify=false", "database.internal", 9440},
		{"ipv4", "clickhouse://192.0.2.10:19000/app", "192.0.2.10", 19000},
		{"ipv6", "clickhouse://[2001:db8::10]:19440/app?secure=true", "2001:db8::10", 19440},
		{"multiple_addresses", "clickhouse://first.internal:19000,second.internal:29000/app", "first.internal", 19000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseClientOptions(tc.dsn, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			before, err := parseClientOptions(tc.dsn, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			host, port := nativeEndpoint(opts)
			if host != tc.host || port != tc.port {
				t.Fatal("advertised endpoint did not match the first driver address")
			}
			if !reflect.DeepEqual(opts, before) {
				t.Fatal("endpoint selection changed driver options")
			}
			client := &Client{host: host, port: port}
			if client.NativeHost() != tc.host || client.NativePort() != tc.port {
				t.Fatal("client did not preserve the selected endpoint")
			}
		})
	}
}
