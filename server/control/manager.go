package control

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/auth"
	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/util"
	"github.com/XY2006DATE/TunnelX/server/proxy"
)

type ClientInfo struct {
	ID            string
	Conn          net.Conn
	Proxies       map[string]*protocol.ProxyConfig
	LastHeartbeat time.Time
	CreatedAt     time.Time
	mu            sync.RWMutex
	sendMu        sync.Mutex
}

type Manager struct {
	clients          map[string]*ClientInfo
	authenticator    *auth.Authenticator
	sessionMgr       *auth.SessionManager
	heartbeatTimeout time.Duration
	proxyManager     *proxy.Manager
	requestManager   *RequestManager
	deletedProxies   map[string]map[string]struct{}
	bindAddr         string
	mu               sync.RWMutex
}

func NewManager(authenticator *auth.Authenticator, heartbeatTimeout time.Duration, proxyMgr *proxy.Manager, requestMgr *RequestManager, bindAddr string) *Manager {
	mgr := &Manager{
		clients:          make(map[string]*ClientInfo),
		authenticator:    authenticator,
		sessionMgr:       auth.NewSessionManager(),
		heartbeatTimeout: heartbeatTimeout,
		proxyManager:     proxyMgr,
		requestManager:   requestMgr,
		deletedProxies:   make(map[string]map[string]struct{}),
		bindAddr:         bindAddr,
	}

	go mgr.heartbeatChecker()
	return mgr
}

func (m *Manager) HandleClient(conn net.Conn) {
	msg, err := protocol.RecvMessage(conn)
	if err != nil {
		util.Error("Failed to receive register: %v", err)
		conn.Close()
		return
	}

	if msg.Type == protocol.TypeStartWorkConn {
		var req protocol.StartWorkConn
		if err := msg.ParseData(&req); err != nil {
			conn.Close()
			return
		}
		if err := m.proxyManager.RegisterWorkConn(req.ConnectionID, conn); err != nil {
			util.Error("Failed to register work conn: %v", err)
		}
		return
	}

	if msg.Type != protocol.TypeRegister {
		util.Warn("Invalid message type: %s", msg.Type)
		m.sendError(conn, "expected register message")
		conn.Close()
		return
	}

	var req protocol.RegisterRequest
	if err := msg.ParseData(&req); err != nil {
		util.Error("Failed to parse register: %v", err)
		m.sendError(conn, "invalid register request")
		return
	}

	util.Info("Client register: id=%s, proxies=%d", req.ClientID, len(req.Proxies))

	if !m.authenticator.Verify(req.Token) {
		util.Warn("Authentication failed: %s", req.ClientID)
		m.sendRegisterResponse(conn, false, "authentication failed", nil)
		return
	}

	m.mu.Lock()
	if existingClient, exists := m.clients[req.ClientID]; exists {
		existingClient.Conn.Close()
		delete(m.clients, req.ClientID)
		util.Info("Replaced existing client: %s", req.ClientID)
	}

	client := &ClientInfo{
		ID:            req.ClientID,
		Conn:          conn,
		Proxies:       make(map[string]*protocol.ProxyConfig),
		LastHeartbeat: time.Now(),
		CreatedAt:     time.Now(),
	}

	deleted := make([]string, 0)
	tombstones := m.deletedProxies[req.ClientID]
	for i := range req.Proxies {
		proxy := &req.Proxies[i]
		if _, wasDeleted := tombstones[proxy.Name]; wasDeleted {
			deleted = append(deleted, proxy.Name)
			continue
		}
		client.Proxies[proxy.Name] = proxy
	}

	m.clients[req.ClientID] = client
	m.mu.Unlock()

	m.sessionMgr.CreateSession(req.ClientID, req.Token)

	if err := m.sendRegisterResponse(conn, true, "register success", deleted); err != nil {
		util.Error("Failed to send register response: %v", err)
		m.RemoveClient(req.ClientID)
		return
	}

	util.Info("Client registered: %s (%d proxies)", req.ClientID, len(client.Proxies))
	m.setupProxies(req.ClientID, client.Proxies, m.bindAddr, m.proxyManager)

	m.handleHeartbeat(client)
}

func (m *Manager) handleHeartbeat(client *ClientInfo) {
	for {
		msg, err := protocol.RecvMessage(client.Conn)
		if err != nil {
			util.Info("Client disconnected: %s (%v)", client.ID, err)
			m.RemoveClient(client.ID)
			return
		}

		switch msg.Type {
		case protocol.TypeHeartbeat:
			client.mu.Lock()
			client.LastHeartbeat = time.Now()
			client.mu.Unlock()

			m.sessionMgr.UpdateLastSeen(client.ID)

			response, _ := protocol.NewMessage(protocol.TypeHeartbeatAck, &protocol.HeartbeatResponse{
				Success: true,
			})
			if err := m.sendToClient(client, response); err != nil {
				util.Error("Failed to send heartbeat response: %v", err)
				m.RemoveClient(client.ID)
				return
			}

			util.Debug("Heartbeat received: %s", client.ID)

		case protocol.TypeProxyReqNew:
			// 客户端请求新代理
			var reqMsg protocol.ProxyRequestMessage
			if err := msg.ParseData(&reqMsg); err != nil {
				util.Error("Failed to parse proxy request: %v", err)
				continue
			}

			// 创建请求记录
			proxyReq := &ProxyRequest{
				ID:         reqMsg.RequestID,
				ClientID:   client.ID,
				ClientAddr: client.Conn.RemoteAddr().String(),
				LocalIP:    reqMsg.LocalIP,
				LocalPort:  reqMsg.LocalPort,
				ProxyType:  reqMsg.ProxyType,
				ProxyName:  reqMsg.ProxyName,
			}

			if err := m.requestManager.AddRequest(proxyReq); err != nil {
				util.Error("Failed to add request: %v", err)
				// 发送错误响应
				errResp, _ := protocol.NewMessage(protocol.TypeError, &protocol.ErrorMessage{
					Code:    500,
					Message: fmt.Sprintf("Failed to create request: %v", err),
				})
				protocol.SendMessage(client.Conn, errResp)
				continue
			}

			util.Info("Proxy request received: id=%s, client=%s, port=%d",
				reqMsg.RequestID, client.ID, reqMsg.LocalPort)

		case protocol.TypeProxyDelete:
			var deleted protocol.ProxyDeleteMessage
			if err := msg.ParseData(&deleted); err != nil || deleted.ProxyName == "" {
				util.Warn("Invalid proxy delete request from %s", client.ID)
				continue
			}
			if err := m.DeleteClientProxy(client.ID, deleted.ProxyName); err != nil {
				util.Warn("Client proxy delete failed: %v", err)
			}

		case protocol.TypeClose:
			util.Info("Client requested close: %s", client.ID)
			m.RemoveClient(client.ID)
			return

		default:
			util.Warn("Unexpected message type: %s from %s", msg.Type, client.ID)
		}
	}
}

func (m *Manager) heartbeatChecker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		m.checkTimeouts()
	}
}

func (m *Manager) checkTimeouts() {
	m.mu.RLock()
	var timeoutClients []string
	now := time.Now()

	for clientID, client := range m.clients {
		client.mu.RLock()
		if now.Sub(client.LastHeartbeat) > m.heartbeatTimeout {
			timeoutClients = append(timeoutClients, clientID)
		}
		client.mu.RUnlock()
	}
	m.mu.RUnlock()

	for _, clientID := range timeoutClients {
		util.Warn("Client heartbeat timeout: %s", clientID)
		m.RemoveClient(clientID)
	}

	m.sessionMgr.CleanupExpiredSessions(m.heartbeatTimeout)
}

func (m *Manager) GetClient(clientID string) (*ClientInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, ok := m.clients[clientID]
	if !ok {
		return nil, fmt.Errorf("client not found: %s", clientID)
	}
	return client, nil
}

func (m *Manager) RemoveClient(clientID string) {
	m.mu.Lock()
	if client, ok := m.clients[clientID]; ok {
		client.mu.RLock()
		proxies := make([]protocol.ProxyConfig, 0, len(client.Proxies))
		for _, item := range client.Proxies {
			proxies = append(proxies, *item)
		}
		client.mu.RUnlock()
		client.Conn.Close()
		delete(m.clients, clientID)
		m.sessionMgr.DeleteSession(clientID)
		m.mu.Unlock()
		for _, item := range proxies {
			if item.Type == "udp" {
				m.proxyManager.RemoveUDPProxy(item.Name)
			} else {
				m.proxyManager.RemoveProxy(item.Name)
			}
			m.requestManager.ReleasePort(item.RemotePort)
		}
		util.Info("Client removed: %s", clientID)
		return
	}
	m.mu.Unlock()
}

// DeleteClient permanently removes an online client's proxies from this server
// and asks the client to remove the matching local server configuration.
func (m *Manager) DeleteClient(clientID string) error {
	m.mu.Lock()
	client := m.clients[clientID]
	if client == nil {
		m.mu.Unlock()
		return fmt.Errorf("client not found: %s", clientID)
	}
	client.mu.Lock()
	proxies := make([]protocol.ProxyConfig, 0, len(client.Proxies))
	proxyNames := make([]string, 0, len(client.Proxies))
	if m.deletedProxies[clientID] == nil {
		m.deletedProxies[clientID] = make(map[string]struct{})
	}
	for name, item := range client.Proxies {
		proxies = append(proxies, *item)
		proxyNames = append(proxyNames, name)
		m.deletedProxies[clientID][name] = struct{}{}
	}
	client.Proxies = make(map[string]*protocol.ProxyConfig)
	client.mu.Unlock()
	m.mu.Unlock()

	for _, item := range proxies {
		if item.Type == "udp" {
			m.proxyManager.RemoveUDPProxy(item.Name)
		} else {
			m.proxyManager.RemoveProxy(item.Name)
		}
		m.requestManager.ReleasePort(item.RemotePort)
	}
	message, _ := protocol.NewMessage(protocol.TypeClientDelete, &protocol.ClientDeleteMessage{ProxyNames: proxyNames})
	if err := m.sendToClient(client, message); err != nil {
		util.Warn("Client deletion sync failed; it will be reconciled on reconnect: client=%s, error=%v", clientID, err)
	}
	m.RemoveClient(clientID)
	util.Info("Client deleted: %s, proxies=%d", clientID, len(proxies))
	return nil
}

func (m *Manager) GetAllClients() []*ClientInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	clients := make([]*ClientInfo, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	return clients
}

// ClientInfoDTO Dashboard展示用的客户端信息
type ClientInfoDTO struct {
	ID            string
	Address       string
	ConnectedAt   time.Time
	LastHeartbeat time.Time
	ProxiesCount  int
}

type ProxyInfoDTO struct {
	Name, Type, ClientID, LocalIP string
	LocalPort, RemotePort         int
}

func (m *Manager) GetProxyInfoList() []ProxyInfoDTO {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ProxyInfoDTO, 0)
	for _, client := range m.clients {
		client.mu.RLock()
		for _, p := range client.Proxies {
			result = append(result, ProxyInfoDTO{Name: p.Name, Type: p.Type, ClientID: client.ID, LocalIP: p.LocalIP, LocalPort: p.LocalPort, RemotePort: p.RemotePort})
		}
		client.mu.RUnlock()
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ClientID < result[j].ClientID
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func (m *Manager) DeleteProxy(name string) error {
	m.mu.RLock()
	ownerID := ""
	for _, client := range m.clients {
		client.mu.RLock()
		if _, ok := client.Proxies[name]; ok {
			ownerID = client.ID
			client.mu.RUnlock()
			break
		}
		client.mu.RUnlock()
	}
	m.mu.RUnlock()
	if ownerID == "" {
		return fmt.Errorf("proxy not found: %s", name)
	}
	return m.DeleteClientProxy(ownerID, name)
}

// DeleteClientProxy only permits a client to delete one of its own proxies.
func (m *Manager) DeleteClientProxy(clientID, name string) error {
	m.mu.Lock()
	client := m.clients[clientID]
	if m.deletedProxies[clientID] == nil {
		m.deletedProxies[clientID] = make(map[string]struct{})
	}
	m.deletedProxies[clientID][name] = struct{}{}
	m.mu.Unlock()
	if client == nil {
		return fmt.Errorf("client not found: %s", clientID)
	}

	client.mu.Lock()
	proxyConfig, ok := client.Proxies[name]
	if !ok {
		client.mu.Unlock()
		confirmation, _ := protocol.NewMessage(protocol.TypeProxyDelete, &protocol.ProxyDeleteMessage{ProxyName: name})
		if err := m.sendToClient(client, confirmation); err != nil {
			return fmt.Errorf("proxy already absent but confirmation failed: %w", err)
		}
		util.Info("Confirmed already deleted proxy: client=%s, name=%s", clientID, name)
		return nil
	}
	remotePort := proxyConfig.RemotePort
	proxyType := proxyConfig.Type
	delete(client.Proxies, name)
	client.mu.Unlock()

	if proxyType == "udp" {
		m.proxyManager.RemoveUDPProxy(name)
	} else {
		m.proxyManager.RemoveProxy(name)
	}
	m.requestManager.ReleasePort(remotePort)

	confirmation, _ := protocol.NewMessage(protocol.TypeProxyDelete, &protocol.ProxyDeleteMessage{ProxyName: name})
	if err := m.sendToClient(client, confirmation); err != nil {
		return fmt.Errorf("proxy deleted but confirmation failed: %w", err)
	}
	util.Info("Client deleted proxy: client=%s, name=%s, released_port=%d", clientID, name, remotePort)
	return nil
}

func (m *Manager) UpdateProxy(name string, remotePort int) error {
	m.mu.RLock()
	var owner *ClientInfo
	var updated protocol.ProxyConfig
	for _, client := range m.clients {
		client.mu.RLock()
		if p, ok := client.Proxies[name]; ok {
			owner = client
			updated = *p
		}
		client.mu.RUnlock()
		if owner != nil {
			break
		}
	}
	m.mu.RUnlock()
	if owner == nil {
		return fmt.Errorf("proxy not found: %s", name)
	}
	m.proxyManager.RemoveProxy(name)
	updated.RemotePort = remotePort
	if err := m.SetupApprovedProxy(owner.ID, &updated); err != nil {
		return err
	}
	msg, _ := protocol.NewMessage(protocol.TypeProxyApproval, &protocol.ProxyApprovalMessage{RequestID: fmt.Sprintf("update-%d", time.Now().UnixNano()), Approved: true, ProxyName: updated.Name, LocalIP: updated.LocalIP, LocalPort: updated.LocalPort, ProxyType: updated.Type, RemotePort: updated.RemotePort})
	return m.sendToClient(owner, msg)
}

// GetClientInfoList 获取所有客户端信息（用于Dashboard）
func (m *Manager) GetClientInfoList() []ClientInfoDTO {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]ClientInfoDTO, 0, len(m.clients))
	for _, client := range m.clients {
		client.mu.RLock()
		// Dashboard only exposes clients that own at least one approved proxy.
		if len(client.Proxies) == 0 {
			client.mu.RUnlock()
			continue
		}
		dto := ClientInfoDTO{
			ID:            client.ID,
			Address:       client.Conn.RemoteAddr().String(),
			ConnectedAt:   client.CreatedAt,
			LastHeartbeat: client.LastHeartbeat,
			ProxiesCount:  len(client.Proxies),
		}
		client.mu.RUnlock()
		result = append(result, dto)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (m *Manager) sendRegisterResponse(conn net.Conn, success bool, message string, deleted []string) error {
	resp := &protocol.RegisterResponse{
		Success:        success,
		Message:        message,
		ServerVersion:  "1.0.0",
		DeletedProxies: deleted,
	}
	msg, _ := protocol.NewMessage(protocol.TypeRegisterAck, resp)
	return protocol.SendMessage(conn, msg)
}

func (m *Manager) sendToClient(client *ClientInfo, msg *protocol.Message) error {
	client.sendMu.Lock()
	defer client.sendMu.Unlock()
	return protocol.SendMessage(client.Conn, msg)
}

func (m *Manager) sendError(conn net.Conn, message string) {
	errMsg := &protocol.ErrorMessage{
		Code:    400,
		Message: message,
	}
	msg, _ := protocol.NewMessage(protocol.TypeError, errMsg)
	protocol.SendMessage(conn, msg)
}

// SendProxyApproval 发送代理审批消息给客户端
func (m *Manager) SendProxyApproval(clientID string, approval *protocol.ProxyApprovalMessage) error {
	client, err := m.GetClient(clientID)
	if err != nil {
		return fmt.Errorf("get client: %w", err)
	}

	msg, err := protocol.NewMessage(protocol.TypeProxyApproval, approval)
	if err != nil {
		return fmt.Errorf("create message: %w", err)
	}

	if err := m.sendToClient(client, msg); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	util.Info("Proxy approval sent: client=%s, request=%s, approved=%v",
		clientID, approval.RequestID, approval.Approved)

	return nil
}

// GetRequestManager 获取请求管理器
func (m *Manager) GetRequestManager() *RequestManager {
	return m.requestManager
}
