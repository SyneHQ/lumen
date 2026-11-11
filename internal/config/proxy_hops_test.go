package config

import "testing"

func TestTrustedProxyHops(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_HOPS", "")
	if got := Load().TrustedProxyHops; got != 1 {
		t.Fatalf("default = %d, want 1", got)
	}
	t.Setenv("TRUSTED_PROXY_HOPS", "0")
	if got := Load().TrustedProxyHops; got != 0 {
		t.Fatalf("0 = %d", got)
	}
	c := &Config{TrustedProxyHops: -1, DevMode: true}
	if err := c.Validate(); err == nil {
		t.Fatal("negative hops accepted")
	}
}
