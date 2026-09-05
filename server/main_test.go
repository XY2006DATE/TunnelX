package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/soheilhy/cmux"
)

func testSharedPortTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.StartTLS()
	defer server.Close()
	return &tls.Config{
		Certificates: append([]tls.Certificate(nil), server.TLS.Certificates...),
		MinVersion:   tls.VersionTLS12,
	}
}

func TestSharedTLSPortRoutesDecryptedHTTPToDashboard(t *testing.T) {
	baseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer baseListener.Close()

	sharedListener := wrapServerTLS(baseListener, testSharedPortTLSConfig(t))
	multiplexer := cmux.New(sharedListener)
	httpListener := multiplexer.Match(cmux.HTTP1Fast())
	_ = multiplexer.Match(cmux.Any())
	go multiplexer.Serve()

	accepted := make(chan net.Conn, 1)
	acceptErrors := make(chan error, 1)
	go func() {
		conn, acceptErr := httpListener.Accept()
		if acceptErr != nil {
			acceptErrors <- acceptErr
			return
		}
		accepted <- conn
	}()

	client, err := tls.Dial("tcp", baseListener.Addr().String(), &tls.Config{InsecureSkipVerify: true}) // test certificate
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("GET /api/setup-status HTTP/1.1\r\nHost: localhost\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case conn := <-accepted:
		conn.Close()
	case err := <-acceptErrors:
		t.Fatalf("dashboard listener accept failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("decrypted HTTPS request was not routed to the dashboard listener")
	}
}
