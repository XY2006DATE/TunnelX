package transport

import "testing"

func TestParseServerEndpointPreservesWebSocketPaths(t *testing.T) {
	tests := []struct {
		raw, address string
		port         int
		secure, ws   bool
	}{
		{"127.0.0.1", "127.0.0.1", 7100, false, false},
		{"https://Example.COM:7100/", "example.com", 7100, true, false},
		{"http://example.com:7100/", "example.com", 7100, false, false},
		{"https://Example.COM/proxy", "wss://example.com:443/proxy", 443, true, true},
		{"http://127.0.0.1:7100/proxy", "ws://127.0.0.1:7100/proxy", 7100, false, true},
		{"wss://example.com", "wss://example.com:443/proxy", 443, true, true},
		{"ws://example.com/", "ws://example.com:80/proxy", 80, false, true},
		{"wss://example.com:8443/team%20A/proxy?tenant=one", "wss://example.com:8443/team%20A/proxy?tenant=one", 8443, true, true},
		{"ws://[::1]:7100/proxy", "ws://[::1]:7100/proxy", 7100, false, true},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseServerEndpoint(test.raw, 7100, false)
			if err != nil {
				t.Fatal(err)
			}
			if got.Address != test.address || got.Port != test.port || got.TLS != test.secure || got.WebSocket != test.ws {
				t.Fatalf("got %+v, want %+v", got, test)
			}
		})
	}
	for _, invalid := range []string{"", "ftp://example.com/proxy", "https://user:password@example.com/proxy", "wss://example.com/proxy#fragment", "tcp://example.com/path", "wss://example.com:99999/proxy"} {
		if _, err := ParseServerEndpoint(invalid, 7100, false); err == nil {
			t.Errorf("accepted invalid endpoint %q", invalid)
		}
	}
}
