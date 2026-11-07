package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clientcontrol "github.com/XY2006DATE/TunnelX/client/control"
	"github.com/XY2006DATE/TunnelX/common/auth"
	"github.com/XY2006DATE/TunnelX/common/config"
	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/transport"
	servercontrol "github.com/XY2006DATE/TunnelX/server/control"
	"github.com/XY2006DATE/TunnelX/server/proxy"
)

func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func TestWSSReverseProxyCarriesControlTCPUDPAndHalfClose(t *testing.T) {
	localTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localTCP.Close()
	go func() {
		for {
			conn, err := localTCP.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				data, err := io.ReadAll(conn)
				if err == nil {
					_, _ = conn.Write(append([]byte("response:"), data...))
				}
			}()
		}
	}()
	localUDP, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localUDP.Close()
	go func() {
		buffer := make([]byte, 65536)
		for {
			n, peer, err := localUDP.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = localUDP.WriteTo(buffer[:n], peer)
		}
	}()
	tcpPort, udpPort := unusedPort(t), unusedPort(t)
	for udpPort == tcpPort {
		udpPort = unusedPort(t)
	}
	proxyManager := proxy.NewManager()
	authenticator := auth.NewAuthenticator("websocket-test-token")
	manager := servercontrol.NewManager(authenticator, 15*time.Second, proxyManager, servercontrol.NewRequestManager(servercontrol.NewPortPool(1024, 65535), time.Minute), "127.0.0.1")
	defer proxyManager.RemoveProxy("ws-tcp")
	defer proxyManager.RemoveUDPProxy("ws-udp")
	backend := httptest.NewServer(transport.WebSocketHandler("/proxy", http.NotFoundHandler(), manager.HandleClient))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	reverse := httputil.NewSingleHostReverseProxy(target)
	director := reverse.Director
	var upgrades atomic.Int64
	reverse.Director = func(request *http.Request) {
		director(request)
		request.URL.Path = strings.TrimPrefix(request.URL.Path, "/tunnel")
		if request.Header.Get("Upgrade") == "websocket" {
			upgrades.Add(1)
		}
	}
	edge := httptest.NewTLSServer(reverse)
	defer edge.Close()
	if _, err := x509.ParseCertificate(edge.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "edge-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: edge.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, err := transport.ParseServerEndpoint(edge.URL+"/tunnel/proxy", 7100, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.ClientConfig{ClientID: "ws-integration-client", ServerAddr: endpoint.Address, ServerPort: endpoint.Port}
	cfg.Auth.Token = "websocket-test-token"
	cfg.TLS.Enable = true
	cfg.TLS.CAFile = caPath
	cfg.Heartbeat.Interval = 1
	cfg.Heartbeat.Timeout = 10
	cfg.Proxies = []config.ProxyConfig{
		{Name: "ws-tcp", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: localTCP.Addr().(*net.TCPAddr).Port, RemotePort: tcpPort},
		{Name: "ws-udp", Type: "udp", LocalIP: "127.0.0.1", LocalPort: localUDP.LocalAddr().(*net.UDPAddr).Port, RemotePort: udpPort},
	}
	controller := clientcontrol.NewConnection(cfg)
	defer controller.Close()
	errors := make(chan error, 8)
	controller.SetOnMessage(func(message *protocol.Message) {
		if message.Type == protocol.TypeNewProxy {
			var request protocol.NewProxyRequest
			if err := message.ParseData(&request); err != nil {
				errors <- err
				return
			}
			go func() {
				if err := controller.HandleNewProxy(&request); err != nil {
					errors <- err
				}
			}()
		}
	})
	if err := controller.Connect(); err != nil {
		t.Fatal(err)
	}
	// Existing sessions must keep working after a token change; no second token
	// check at every WebSocket upgrade may invalidate their new work channels.
	authenticator.UpdateToken("new-token-for-new-clients")
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tcpPort), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	payload := bytes.Repeat([]byte("request\x00payload"), 10000)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response, append([]byte("response:"), payload...)) {
		t.Fatal("TCP half-close or binary stream failed through WSS reverse proxy")
	}
	udp, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", udpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_ = udp.SetDeadline(time.Now().Add(5 * time.Second))
	datagram := bytes.Repeat([]byte("UDP\x00"), 200)
	if _, err := udp.Write(datagram); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 65536)
	n, err := udp.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], datagram) {
		t.Fatal("UDP datagram changed through WSS")
	}
	if upgrades.Load() < 3 {
		t.Fatalf("control/work channels did not all upgrade: %d", upgrades.Load())
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
	badCfg := *cfg
	badCfg.ClientID = "invalid-token-client"
	badCfg.Auth.Token = "wrong-token"
	badCfg.Proxies = nil
	bad := clientcontrol.NewConnection(&badCfg)
	defer bad.Close()
	if err := bad.Connect(); err == nil {
		t.Fatal("WebSocket transport bypassed token authentication")
	}
}
