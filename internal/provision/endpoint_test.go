package provision

import (
	"testing"

	"github.com/SyneHQ/lumen/internal/ch"
)

func TestAdvertisedHostFallbackAndOverride(t *testing.T) {
	client := &ch.Client{}
	for _, tc := range []struct{ name, override, want string }{
		{"client_default", "", client.NativeHost()},
		{"explicit_dns", "public.example.test", "public.example.test"},
		{"explicit_ipv6", "2001:db8::20", "2001:db8::20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewAdminService(client, nil, "", tc.override, 19440)
			if service.chHost != tc.want || service.chPort != 19440 {
				t.Fatal("advertised endpoint did not preserve the selected host and explicit port")
			}
		})
	}
}
