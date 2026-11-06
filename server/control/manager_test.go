package control

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/XY2006DATE/TunnelX/common/auth"
	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/server/proxy"
)

func TestValidateHTTPSProxyRequiresServerTLS(t *testing.T) {
	requestManager := NewRequestManager(NewPortPool(24000, 24100), time.Hour)
	withoutTLS := NewManager(auth.NewAuthenticator("test-token"), time.Hour, proxy.NewManager(), requestManager, "127.0.0.1")
	if err := withoutTLS.ValidateProxyType("https"); err == nil {
		t.Fatal("HTTPS proxy was accepted without server TLS")
	}

	serverTLS := &tls.Config{Certificates: []tls.Certificate{{}}, MinVersion: tls.VersionTLS12}
	withTLS := NewManager(auth.NewAuthenticator("test-token"), time.Hour, proxy.NewManager(), requestManager, "127.0.0.1", serverTLS)
	if err := withTLS.ValidateProxyType("https"); err != nil {
		t.Fatalf("HTTPS proxy was rejected with server TLS: %v", err)
	}
}

func TestRegisterReturnsProxiesDeletedWhileClientWasOffline(t *testing.T) {
	requestManager := NewRequestManager(NewPortPool(24000, 24100), time.Hour)
	manager := NewManager(auth.NewAuthenticator("test-token"), time.Hour, proxy.NewManager(), requestManager, "127.0.0.1")
	manager.deletedProxies["offline-client"] = map[string]struct{}{"proxy-6349": {}}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go manager.HandleClient(serverConn)

	message, err := protocol.NewMessage(protocol.TypeRegister, &protocol.RegisterRequest{
		ClientID: "offline-client",
		Token:    "test-token",
		Version:  "1.0.0",
		Proxies: []protocol.ProxyConfig{{
			Name: "proxy-6349", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 6349, RemotePort: 16349,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.SendMessage(clientConn, message); err != nil {
		t.Fatal(err)
	}

	responseMessage, err := protocol.RecvMessage(clientConn)
	if err != nil {
		t.Fatal(err)
	}
	var response protocol.RegisterResponse
	if err := responseMessage.ParseData(&response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || len(response.DeletedProxies) != 1 || response.DeletedProxies[0] != "proxy-6349" {
		t.Fatalf("unexpected register response: %+v", response)
	}
	client, err := manager.GetClient("offline-client")
	if err != nil {
		t.Fatal(err)
	}
	if len(client.Proxies) != 0 {
		t.Fatalf("deleted proxy was restored on reconnect: %+v", client.Proxies)
	}
}

func TestOldControlConnectionDoesNotRemoveReplacement(t *testing.T) {
	proxyManager := proxy.NewManager()
	requestManager := NewRequestManager(NewPortPool(24000, 24100), time.Hour)
	manager := NewManager(auth.NewAuthenticator("test-token"), time.Hour, proxyManager, requestManager, "127.0.0.1")
	register, _ := protocol.NewMessage(protocol.TypeRegister, &protocol.RegisterRequest{
		ClientID: "same-client", Token: "test-token",
		Proxies: []protocol.ProxyConfig{{Name: "test-route", Type: "tcp", RemotePort: 0}},
	})
	dial := func() (net.Conn, <-chan struct{}) {
		server, client := net.Pipe()
		t.Cleanup(func() { client.Close() })
		_ = client.SetDeadline(time.Now().Add(2 * time.Second))
		done := make(chan struct{})
		go func() {
			defer close(done)
			manager.HandleClient(server)
		}()
		if err := protocol.SendMessage(client, register); err != nil {
			t.Fatal(err)
		}
		ack, err := protocol.RecvMessage(client)
		if err != nil || ack.Type != protocol.TypeRegisterAck {
			t.Fatalf("registration: %v, %v", ack, err)
		}
		return client, done
	}

	_, oldDone := dial()
	replacement, _ := dial()
	select {
	case <-oldDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old control connection was not closed")
	}
	heartbeat, _ := protocol.NewMessage(protocol.TypeHeartbeat, &protocol.HeartbeatRequest{ClientID: "same-client"})
	if err := protocol.SendMessage(replacement, heartbeat); err != nil {
		t.Fatal(err)
	}
	ack, err := protocol.RecvMessage(replacement)
	if err != nil || ack.Type != protocol.TypeHeartbeatAck {
		t.Fatalf("replacement heartbeat: %v, %v", ack, err)
	}
	if _, ok := proxyManager.GetProxy("test-route"); !ok {
		t.Fatal("replacement lost its proxy")
	}
}

func TestDeleteClientSendsDeletionAndRemovesClient(t *testing.T) {
	requestManager := NewRequestManager(NewPortPool(24000, 24100), time.Hour)
	manager := NewManager(auth.NewAuthenticator("test-token"), time.Hour, proxy.NewManager(), requestManager, "127.0.0.1")
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	manager.clients["delete-client"] = &ClientInfo{
		ID:        "delete-client",
		Conn:      serverConn,
		Proxies:   map[string]*protocol.ProxyConfig{"proxy-a": {Name: "proxy-a", Type: "tcp", RemotePort: 24001}},
		CreatedAt: time.Now(),
	}
	received := make(chan *protocol.Message, 1)
	go func() {
		message, _ := protocol.RecvMessage(clientConn)
		received <- message
	}()
	if err := manager.DeleteClient("delete-client"); err != nil {
		t.Fatal(err)
	}
	message := <-received
	if message == nil || message.Type != protocol.TypeClientDelete {
		t.Fatalf("unexpected deletion message: %+v", message)
	}
	var deleted protocol.ClientDeleteMessage
	if err := message.ParseData(&deleted); err != nil {
		t.Fatal(err)
	}
	if len(deleted.ProxyNames) != 1 || deleted.ProxyNames[0] != "proxy-a" {
		t.Fatalf("unexpected deleted proxies: %+v", deleted.ProxyNames)
	}
	if _, err := manager.GetClient("delete-client"); err == nil {
		t.Fatal("client remains after deletion")
	}
	if _, ok := manager.deletedProxies["delete-client"]["proxy-a"]; !ok {
		t.Fatal("proxy deletion was not retained for reconnect reconciliation")
	}
}
