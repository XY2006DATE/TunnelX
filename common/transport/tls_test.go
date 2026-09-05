package transport

import (
	"crypto/tls"
	"testing"
)

func TestTLSClientUsesCertificateVerificationWithSystemRoots(t *testing.T) {
	cfg, err := NewTLSClientConfig("", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("TLS certificate verification is disabled")
	}
	if cfg.RootCAs != nil {
		t.Fatal("empty CA file should leave RootCAs nil so Go uses the system trust store")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Fatal("TLS versions older than 1.2 are enabled")
	}
}
