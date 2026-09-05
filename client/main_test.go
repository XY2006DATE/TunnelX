package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/XY2006DATE/TunnelX/common/config"
)

func TestNewClientRestoresMultipleServerSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	data := []byte(`client_id: test-client
server_port: 7100
log:
  level: error
dashboard:
  enable: false
  port: 7101
proxies:
  - server_addr: 10.0.0.1
    server_port: 7100
    server_token: token-a
    name: proxy-a
    type: tcp
    local_ip: 127.0.0.1
    local_port: 8080
    remote_port: 18080
  - server_addr: 10.0.0.2
    server_port: 7100
    server_token: token-b
    name: proxy-b
    type: tcp
    local_ip: 127.0.0.1
    local_port: 8081
    remote_port: 18081
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(client.sessions); got != 2 {
		t.Fatalf("sessions=%d, want 2", got)
	}
	if client.sessions["10.0.0.1:7100"] == nil || client.sessions["10.0.0.2:7100"] == nil {
		t.Fatal("server sessions were not restored independently")
	}
}

func TestNormalizeServerEndpoint(t *testing.T) {
	tests := []struct {
		input       string
		fallback    int
		fallbackTLS bool
		wantHost    string
		wantPort    int
		wantTLS     bool
	}{
		{"47.120.67.143", 7100, false, "47.120.67.143", 7100, false},
		{"47.120.67.143:7200", 7100, true, "47.120.67.143", 7200, true},
		{"http://47.120.67.143:7300/", 7100, true, "47.120.67.143", 7300, false},
		{"https://xcloudy.cn:7100/", 7200, false, "xcloudy.cn", 7100, true},
		{"TLS://Example.COM:7400", 7100, false, "example.com", 7400, true},
	}
	for _, tt := range tests {
		host, port, tlsEnabled, err := normalizeServerEndpoint(tt.input, tt.fallback, tt.fallbackTLS)
		if err != nil {
			t.Fatalf("normalize %q: %v", tt.input, err)
		}
		if host != tt.wantHost || port != tt.wantPort || tlsEnabled != tt.wantTLS {
			t.Fatalf("normalize %q = %s:%d tls=%t, want %s:%d tls=%t", tt.input, host, port, tlsEnabled, tt.wantHost, tt.wantPort, tt.wantTLS)
		}
	}
	if _, _, _, err := normalizeServerEndpoint("ftp://xcloudy.cn:7100", 7100, false); err == nil {
		t.Fatal("unsupported server URL scheme was accepted")
	}
}

func TestDisconnectedSessionRejectsDifferentToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	data := []byte(`client_id: token-check-client
log:
  level: error
dashboard:
  enable: false
proxies:
  - server_addr: 127.0.0.1
    server_port: 7100
    server_token: original-token
    name: proxy-a
    type: tcp
    local_ip: 127.0.0.1
    local_port: 8001
    remote_port: 18001
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.getOrCreateSession("127.0.0.1", 7100, "different-token", false); err == nil {
		t.Fatal("disconnected session accepted a different token")
	}
}

func TestHTTPSProxyRestoresTLSForSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	data := []byte(`client_id: tls-client
log:
  level: error
dashboard:
  enable: false
proxies:
  - server_addr: xcloudy.cn
    server_port: 7100
    server_token: test-token
    server_tls: true
    name: proxy-https
    type: tcp
    local_ip: 127.0.0.1
    local_port: 8080
    remote_port: 18080
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	session := client.sessions["xcloudy.cn:7100"]
	if session == nil || !session.tls {
		t.Fatal("HTTPS proxy did not restore a TLS session")
	}
	if !client.sessionConfig(session.addr, session.port, session.token, session.tls, nil).TLS.Enable {
		t.Fatal("TLS session controller did not enable TLS")
	}
}

func TestSessionTokenReuseRules(t *testing.T) {
	if !canReuseSession("original-token", "rotated-token", true) {
		t.Fatal("connected session should survive server token rotation")
	}
	if canReuseSession("original-token", "rotated-token", false) {
		t.Fatal("disconnected session should reject a different token")
	}
	if !canReuseSession("original-token", "original-token", false) {
		t.Fatal("disconnected session should accept its configured token")
	}
}

func TestConfiguredTLSOverridesGlobalDefault(t *testing.T) {
	enabled, disabled := true, false
	if !configuredTLS(&enabled, false) {
		t.Fatal("explicit TLS setting was ignored")
	}
	if configuredTLS(&disabled, true) {
		t.Fatal("explicit plain-text setting was ignored")
	}
	if !configuredTLS(nil, true) {
		t.Fatal("missing per-server TLS setting did not inherit the global default")
	}
}

func TestDeleteProxyOfflinePersistsDeferredServerSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	data := []byte(`client_id: offline-delete-client
server_port: 7100
log:
  level: error
dashboard:
  enable: false
proxies:
  - server_addr: 127.0.0.1
    server_port: 7999
    server_token: test-token
    name: proxy-6349
    type: tcp
    local_ip: 127.0.0.1
    local_port: 6349
    remote_port: 16349
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.deleteProxy("proxy-6349", "127.0.0.1", 7999); err != nil {
		t.Fatal(err)
	}
	if len(client.config.Proxies) != 0 {
		t.Fatalf("proxy was not deleted locally: %+v", client.config.Proxies)
	}
	if len(client.config.PendingDeletes) != 1 || client.config.PendingDeletes[0].Name != "proxy-6349" {
		t.Fatalf("deferred deletion was not persisted: %+v", client.config.PendingDeletes)
	}
	reloaded, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Proxies) != 0 || len(reloaded.PendingDeletes) != 1 {
		t.Fatalf("unexpected persisted state: proxies=%+v pending=%+v", reloaded.Proxies, reloaded.PendingDeletes)
	}
	key := sessionKey("127.0.0.1", 7999)
	session := client.sessions[key]
	client.clearPendingDelete(key, "proxy-6349")
	client.removeIdleSession(session)
	if client.sessions[key] != nil {
		t.Fatal("idle server session remained connected after deletion sync")
	}
}

func TestServerClientDeletionClearsOnlyThatServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")
	data := []byte(`client_id: multi-server-delete-client
server_port: 7100
log:
  level: error
dashboard:
  enable: false
proxies:
  - server_addr: 127.0.0.1
    server_port: 7100
    server_token: token-a
    name: proxy-a
    type: tcp
    local_ip: 127.0.0.1
    local_port: 8001
    remote_port: 18001
  - server_addr: 127.0.0.1
    server_port: 7200
    server_token: token-b
    name: proxy-b
    type: tcp
    local_ip: 127.0.0.1
    local_port: 8002
    remote_port: 18002
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(path)
	if err != nil {
		t.Fatal(err)
	}
	removedKey := sessionKey("127.0.0.1", 7100)
	client.removeServerConfiguration(client.sessions[removedKey])
	if client.sessions[removedKey] != nil {
		t.Fatal("deleted server session remains")
	}
	if len(client.config.Proxies) != 1 || client.config.Proxies[0].Name != "proxy-b" {
		t.Fatalf("wrong remaining proxies: %+v", client.config.Proxies)
	}
	reloaded, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Proxies) != 1 || reloaded.Proxies[0].Name != "proxy-b" {
		t.Fatalf("wrong persisted proxies: %+v", reloaded.Proxies)
	}
}
