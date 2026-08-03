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
		want     string
	}{
		{
			name:     "uses first X-Forwarded-For entry behind a proxy chain",
			headers:  http.Header{"X-Forwarded-For": []string{"203.0.113.5, 10.0.0.1, 10.0.0.2"}},
			peerAddr: "10.0.0.2:443",
			want:     "203.0.113.5",
		},
		{
			name:     "falls back to peer addr when no XFF header",
			headers:  http.Header{},
			peerAddr: "198.51.100.7:50051",
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
			got := resolveClientIP(tc.headers, tc.peerAddr)
			if got != tc.want {
				t.Errorf("resolveClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
