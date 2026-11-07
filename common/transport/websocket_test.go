package transport

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocketByteStreamAndHalfClose(t *testing.T) {
	result := make(chan error, 1)
	server := httptest.NewServer(WebSocketHandler("/proxy", http.NotFoundHandler(), func(conn net.Conn) {
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		data, err := io.ReadAll(conn)
		if err == nil {
			_, err = conn.Write(append([]byte("reply:"), data...))
		}
		if err == nil {
			err = conn.(interface{ CloseWrite() error }).CloseWrite()
		}
		result <- err
	}))
	defer server.Close()
	conn, err := DialWebSocket("ws"+strings.TrimPrefix(server.URL, "http")+"/proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload := bytes.Repeat([]byte("binary\x00stream"), 10000)
	for offset := 0; offset < len(payload); {
		end := offset + 271
		if end > len(payload) {
			end = len(payload)
		}
		if _, err := conn.Write(payload[offset:end]); err != nil {
			t.Fatal(err)
		}
		offset = end
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append([]byte("reply:"), payload...)) {
		t.Fatal("byte stream or half-close lost data")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("late")); err == nil {
		t.Fatal("write after half-close succeeded")
	}
}

func TestWebSocketTLSVerificationAndRouting(t *testing.T) {
	accepted := make(chan net.Conn, 2)
	server := httptest.NewTLSServer(WebSocketHandler("/proxy", http.NotFoundHandler(), func(conn net.Conn) { accepted <- conn }))
	defer server.Close()
	address := "wss" + strings.TrimPrefix(server.URL, "https") + "/proxy"
	if conn, err := DialWebSocket(address, nil); err == nil {
		conn.Close()
		t.Fatal("untrusted TLS certificate was accepted")
	}
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	conn, err := DialWebSocket(address, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer (<-accepted).Close()
	response, err := server.Client().Get(server.URL + "/proxy")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("non-upgrade status %d", response.StatusCode)
	}
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: pool}, Subprotocols: []string{WebSocketSubprotocol}}
	bad, response, err := dialer.Dial(address, http.Header{"Origin": []string{"https://untrusted.example"}})
	if err == nil {
		bad.Close()
		t.Fatal("browser-originated connection accepted")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin rejection: %v, %v", response, err)
	}
	if response.Body != nil {
		response.Body.Close()
	}
	dialer.Subprotocols = nil
	bad, response, err = dialer.Dial(address, nil)
	if err == nil {
		bad.Close()
		t.Fatal("missing subprotocol accepted")
	}
	if response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("subprotocol rejection: %v", err)
	}
	response.Body.Close()
}
