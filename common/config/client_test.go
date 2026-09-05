package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadClientConfigCreatesDashboardOnlyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "client.yaml")
	config, err := LoadClientConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ServerAddr != "" || config.ServerPort != 0 || config.Auth.Token != "" {
		t.Fatalf("new client has an unexpected default server: %+v", config)
	}
	if !config.Dashboard.Enable || config.Dashboard.Port != 7101 {
		t.Fatalf("dashboard defaults are incorrect: %+v", config.Dashboard)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("default config file is empty")
	}
	text := string(data)
	if strings.Contains(text, "server_addr:") || strings.Contains(text, "server_port:") || strings.Contains(text, "auth:") {
		t.Fatalf("default config contains a default connection:\n%s", text)
	}
}

func TestLoadClientConfigAllowsEmptyServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.yaml")
	if err := os.WriteFile(path, []byte("dashboard:\n  enable: true\n  port: 7101\nproxies: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadClientConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ServerAddr != "" || config.ServerPort != 0 {
		t.Fatalf("empty server configuration was changed: %+v", config)
	}
}
