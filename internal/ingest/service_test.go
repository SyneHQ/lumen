package ingest

import (
	"net/http"
	"testing"
)

func TestResolveClientIP(t *testing.T) {
	cases := []struct {
		name     string
		headers  http.Header
		peerAddr string
		hops     int
		want     string
	}{
		{
			name:     "ignores a spoofed leftmost entry with one trusted hop",
			headers:  http.Header{"X-Forwarded-For": []string{"6.6.6.6, 203.0.113.5"}},
			peerAddr: "10.0.0.2:443",
			hops:     1,
			want:     "203.0.113.5",
		},
		{
			name:     "two hops picks second from the right",
			headers:  http.Header{"X-Forwarded-For": []string{"6.6.6.6, 203.0.113.5, 10.0.0.1"}},
			peerAddr: "10.0.0.2:443",
			hops:     2,
			want:     "203.0.113.5",
		},
		{
			name:     "fewer entries than hops uses the leftmost",
			headers:  http.Header{"X-Forwarded-For": []string{"203.0.113.5"}},
			peerAddr: "10.0.0.2:443",
			hops:     3,
			want:     "203.0.113.5",
		},
		{
			name:     "zero hops ignores the header",
			headers:  http.Header{"X-Forwarded-For": []string{"203.0.113.5"}},
			peerAddr: "198.51.100.7:50051",
			hops:     0,
			want:     "198.51.100.7",
		},
		{
			name:     "IPv6 entries and repeated headers with whitespace",
			headers:  http.Header{"X-Forwarded-For": []string{" 6.6.6.6 ", "  2001:DB8::1  "}},
			peerAddr: "[::1]:443",
			hops:     1,
			want:     "2001:db8::1",
		},
		{
			name:     "unparseable entry falls back to the peer",
			headers:  http.Header{"X-Forwarded-For": []string{"1.2.3.4, garbage"}},
			peerAddr: "[2001:db8::9]:443",
			hops:     1,
			want:     "2001:db8::9",
		},
		{
			name:     "falls back to peer addr when no XFF header",
			headers:  http.Header{},
			peerAddr: "198.51.100.7:50051",
			hops:     1,
			want:     "198.51.100.7",
		},
		{
			name:     "falls back to raw peer addr when it has no port",
			headers:  http.Header{},
			peerAddr: "198.51.100.7",
			want:     "198.51.100.7",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveClientIP(tc.headers, tc.peerAddr, tc.hops)
			if got != tc.want {
				t.Errorf("resolveClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
