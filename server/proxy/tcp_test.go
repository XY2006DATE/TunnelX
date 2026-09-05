package proxy

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

type preparedConnection struct {
	conn net.Conn
	tls  bool
	err  error
}

func testServerTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.StartTLS()
	defer server.Close()
	return &tls.Config{
		Certificates: append([]tls.Certificate(nil), server.TLS.Certificates...),
		MinVersion:   tls.VersionTLS12,
	}
}

func TestHTTPAndHTTPSProxyRequiresCertificate(t *testing.T) {
	if _, err := NewHTTPAndHTTPSProxy("web", 18080, nil, nil); err == nil {
		t.Fatal("HTTP + HTTPS proxy accepted a missing TLS certificate")
	}
}

func TestHTTPAndHTTPSProxyAcceptsPlainHTTP(t *testing.T) {
	proxy, err := NewHTTPAndHTTPSProxy("web", 18080, nil, testServerTLSConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := net.Pipe()
	result := make(chan preparedConnection, 1)
	go func() {
		conn, isTLS, prepareErr := proxy.prepareUserConnection(serverConn)
		result <- preparedConnection{conn: conn, tls: isTLS, err: prepareErr}
	}()

	want := "GET /doc.html HTTP/1.1\r\nHost: xcloudy.cn\r\n\r\n"
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := clientConn.Write([]byte(want))
		writeDone <- writeErr
	}()
	prepared := <-result
	if prepared.err != nil {
		t.Fatal(prepared.err)
	}
	defer prepared.conn.Close()
	if prepared.tls {
		t.Fatal("plain HTTP connection was detected as TLS")
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(prepared.conn, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	clientConn.Close()
	if string(got) != want {
		t.Fatalf("forwarded HTTP = %q, want %q", got, want)
	}
}

func TestHTTPAndHTTPSProxyTerminatesTLS(t *testing.T) {
	proxy, err := NewHTTPAndHTTPSProxy("web", 18080, nil, testServerTLSConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := net.Pipe()
	result := make(chan preparedConnection, 1)
	go func() {
		conn, isTLS, prepareErr := proxy.prepareUserConnection(serverConn)
		result <- preparedConnection{conn: conn, tls: isTLS, err: prepareErr}
	}()

	tlsClient := tls.Client(clientConn, &tls.Config{InsecureSkipVerify: true}) // test-only certificate
	handshake := make(chan error, 1)
	go func() { handshake <- tlsClient.Handshake() }()
	prepared := <-result
	if prepared.err != nil {
		t.Fatal(prepared.err)
	}
	if err := <-handshake; err != nil {
		t.Fatal(err)
	}
	defer prepared.conn.Close()
	if !prepared.tls {
		t.Fatal("TLS connection was not detected")
	}

	want := "GET /doc.html HTTP/1.1\r\nHost: xcloudy.cn\r\n\r\n"
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := tlsClient.Write([]byte(want))
		writeDone <- writeErr
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(prepared.conn, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("decrypted request = %q, want %q", got, want)
	}
	clientConn.Close()
}
