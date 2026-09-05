package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeConfigUpdatesTLSAndReturnsPersistedValues(t *testing.T) {
	initial := RuntimeConfig{Port: 7100}
	var persisted RuntimeConfig
	d := &Dashboard{
		runtimeConfig: initial,
		updateRuntime: func(next RuntimeConfig) error {
			persisted = next
			return nil
		},
	}
	body := []byte(`{"port":7443,"tls_enabled":true,"tls_cert_file":" /certs/fullchain.pem ","tls_key_file":" /certs/private.key "}`)
	request := httptest.NewRequest(http.MethodPost, "/api/runtime-config", bytes.NewReader(body))
	response := httptest.NewRecorder()

	d.handleRuntimeConfig(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	want := RuntimeConfig{
		Port:        7443,
		TLSEnabled:  true,
		TLSCertFile: "/certs/fullchain.pem",
		TLSKeyFile:  "/certs/private.key",
	}
	if persisted != want {
		t.Fatalf("persisted = %#v, want %#v", persisted, want)
	}
	var got RuntimeConfig
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got != want {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}

func TestRuntimeConfigRejectsTLSWithoutCertificateFiles(t *testing.T) {
	called := false
	d := &Dashboard{
		runtimeConfig: RuntimeConfig{Port: 7100},
		updateRuntime: func(next RuntimeConfig) error {
			called = true
			return nil
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/runtime-config", bytes.NewBufferString(`{"tls_enabled":true}`))
	response := httptest.NewRecorder()

	d.handleRuntimeConfig(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if called {
		t.Fatal("invalid TLS configuration was persisted")
	}
}

func TestRuntimeConfigPortOnlyUpdateKeepsTLSSettings(t *testing.T) {
	initial := RuntimeConfig{
		Port:        7100,
		TLSEnabled:  true,
		TLSCertFile: "/certs/fullchain.pem",
		TLSKeyFile:  "/certs/private.key",
	}
	var persisted RuntimeConfig
	d := &Dashboard{
		runtimeConfig: initial,
		updateRuntime: func(next RuntimeConfig) error {
			persisted = next
			return nil
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/runtime-config", bytes.NewBufferString(`{"port":7200}`))
	response := httptest.NewRecorder()

	d.handleRuntimeConfig(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	initial.Port = 7200
	if persisted != initial {
		t.Fatalf("persisted = %#v, want %#v", persisted, initial)
	}
}
