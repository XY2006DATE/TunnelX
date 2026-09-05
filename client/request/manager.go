package request

import (
	"fmt"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/util"
	"github.com/google/uuid"
)

// RequestStatus 请求状态
type RequestStatus string

const (
	StatusPending  RequestStatus = "pending"
	StatusApproved RequestStatus = "approved"
	StatusRejected RequestStatus = "rejected"
)

// ProxyRequest 代理请求
type ProxyRequest struct {
	ServerAddr  string        `json:"server_addr"`
	ServerPort  int           `json:"server_port"`
	ID          string        `json:"id"`
	LocalIP     string        `json:"local_ip"`
	LocalPort   int           `json:"local_port"`
	ProxyType   string        `json:"proxy_type"`
	ProxyName   string        `json:"proxy_name"`
	Status      RequestStatus `json:"status"`
	RemotePort  int           `json:"remote_port,omitempty"`
	Reason      string        `json:"reason,omitempty"`
	RequestTime time.Time     `json:"request_time"`
}

// BindServer records which server owns a request before it is sent.
func (m *Manager) BindServer(requestID, serverAddr string, serverPort int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.requests[requestID]
	if !ok {
		return fmt.Errorf("request not found: %s", requestID)
	}
	req.ServerAddr = serverAddr
	req.ServerPort = serverPort
	return nil
}

// Manager 客户端请求管理器
type Manager struct {
	requests       map[string]*ProxyRequest // requestID -> request
	approvalChan   chan *protocol.ProxyApprovalMessage
	mu             sync.RWMutex
	onProxyApprove func(*ProxyRequest) error // 回调函数，处理批准的代理
}

// NewManager 创建请求管理器
func NewManager() *Manager {
	return &Manager{
		requests:     make(map[string]*ProxyRequest),
		approvalChan: make(chan *protocol.ProxyApprovalMessage, 10),
	}
}

// RestoreApproved rebuilds dashboard history from persisted proxy configuration.
func (m *Manager) RestoreApproved(name, localIP string, localPort int, proxyType string, remotePort int) {
	m.RestoreApprovedWithID("restored-"+name, name, localIP, localPort, proxyType, remotePort, "", 0)
}

func (m *Manager) RestoreApprovedWithID(id, name, localIP string, localPort int, proxyType string, remotePort int, serverAddr string, serverPort int) {
	if localIP == "" {
		localIP = "127.0.0.1"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[id] = &ProxyRequest{ID: id, ServerAddr: serverAddr, ServerPort: serverPort, LocalIP: localIP, LocalPort: localPort, ProxyType: proxyType, ProxyName: name, Status: StatusApproved, RemotePort: remotePort, RequestTime: time.Now()}
}

// CreateRequest 创建新的代理请求
func (m *Manager) CreateRequest(localIP string, localPort int, proxyType, proxyName string) (*ProxyRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if proxyName == "" {
		proxyName = fmt.Sprintf("%s-%d", proxyType, localPort)
	}

	req := &ProxyRequest{
		ID:          uuid.New().String(),
		LocalIP:     localIP,
		LocalPort:   localPort,
		ProxyType:   proxyType,
		ProxyName:   proxyName,
		Status:      StatusPending,
		RequestTime: time.Now(),
	}

	m.requests[req.ID] = req

	util.Info("Created proxy request: id=%s, port=%d, type=%s", req.ID, localPort, proxyType)

	return req, nil
}

// HandleApproval 处理服务端的审批消息
func (m *Manager) HandleApproval(approval *protocol.ProxyApprovalMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	req, exists := m.requests[approval.RequestID]
	if !exists {
		if !approval.Approved || approval.LocalPort == 0 {
			return fmt.Errorf("request not found: %s", approval.RequestID)
		}
		req = &ProxyRequest{ID: approval.RequestID, LocalIP: approval.LocalIP, LocalPort: approval.LocalPort, ProxyType: approval.ProxyType, ProxyName: approval.ProxyName, RequestTime: time.Now()}
		m.requests[approval.RequestID] = req
	}

	if approval.Approved {
		req.Status = StatusApproved
		req.RemotePort = approval.RemotePort
		if approval.ProxyName != "" {
			req.ProxyName = approval.ProxyName
		}
		if req.LocalIP == "" {
			req.LocalIP = "127.0.0.1"
		}
		util.Info("Request approved: id=%s, remote_port=%d", req.ID, approval.RemotePort)

		// 调用回调函数处理批准的代理
		if m.onProxyApprove != nil {
			if err := m.onProxyApprove(req); err != nil {
				return fmt.Errorf("setup approved proxy: %w", err)
			}
		}
	} else {
		req.Status = StatusRejected
		req.Reason = approval.Reason
		util.Info("Request rejected: id=%s, reason=%s", req.ID, approval.Reason)
	}

	return nil
}

// GetRequest 获取请求
func (m *Manager) GetRequest(requestID string) (*ProxyRequest, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	req, ok := m.requests[requestID]
	return req, ok
}

// GetAllRequests 获取所有请求
func (m *Manager) GetAllRequests() []*ProxyRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()

	all := make([]*ProxyRequest, 0, len(m.requests))
	for _, req := range m.requests {
		all = append(all, req)
	}
	return all
}

// GetPendingRequests 获取待审批的请求
func (m *Manager) GetPendingRequests() []*ProxyRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pending := make([]*ProxyRequest, 0)
	for _, req := range m.requests {
		if req.Status == StatusPending {
			pending = append(pending, req)
		}
	}
	return pending
}

// RemoveProxy clears request history after the owning server confirms deletion.
func (m *Manager) RemoveProxy(serverAddr string, serverPort int, proxyName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, req := range m.requests {
		if req.ServerAddr == serverAddr && req.ServerPort == serverPort && req.ProxyName == proxyName {
			delete(m.requests, id)
		}
	}
}

// RemoveServer clears all request and approval history owned by one server.
func (m *Manager) RemoveServer(serverAddr string, serverPort int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, req := range m.requests {
		if req.ServerAddr == serverAddr && req.ServerPort == serverPort {
			delete(m.requests, id)
		}
	}
}

func (m *Manager) HasPendingForServer(serverAddr string, serverPort int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, req := range m.requests {
		if req.ServerAddr == serverAddr && req.ServerPort == serverPort && req.Status == StatusPending {
			return true
		}
	}
	return false
}

// SetOnProxyApprove 设置代理批准回调函数
func (m *Manager) SetOnProxyApprove(callback func(*ProxyRequest) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onProxyApprove = callback
}
