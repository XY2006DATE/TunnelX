package control

import (
	"net"
	"testing"
	"time"

	"github.com/XY2006DATE/TunnelX/common/auth"
	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/server/proxy"
)

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
