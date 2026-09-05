package control

import (
	"fmt"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/util"
)

// ProxyRequestStatus 请求状态
type ProxyRequestStatus string

const (
	StatusPending  ProxyRequestStatus = "pending"
	StatusApproved ProxyRequestStatus = "approved"
	StatusRejected ProxyRequestStatus = "rejected"
	StatusTimeout  ProxyRequestStatus = "timeout"
)

// ProxyRequest 代理请求
type ProxyRequest struct {
	ID          string             `json:"id"`
	ClientID    string             `json:"client_id"`
	ClientAddr  string             `json:"client_addr"`
	LocalIP     string             `json:"local_ip"`
	LocalPort   int                `json:"local_port"`
	ProxyType   string             `json:"proxy_type"`
	ProxyName   string             `json:"proxy_name"`
	Status      ProxyRequestStatus `json:"status"`
	RequestTime time.Time          `json:"request_time"`
	RemotePort  int                `json:"remote_port,omitempty"`
	Reason      string             `json:"reason,omitempty"`
}

// RequestManager 请求管理器
type RequestManager struct {
	requests map[string]*ProxyRequest // requestID -> request
	portPool *PortPool
	timeout  time.Duration
	mu       sync.RWMutex
}

// NewRequestManager 创建请求管理器
func NewRequestManager(portPool *PortPool, timeout time.Duration) *RequestManager {
	if timeout == 0 {
		timeout = 30 * time.Minute // 默认30分钟超时
	}

	rm := &RequestManager{
		requests: make(map[string]*ProxyRequest),
		portPool: portPool,
		timeout:  timeout,
	}

	go rm.cleanupExpired()
	return rm
}

// AddRequest 添加新请求
func (rm *RequestManager) AddRequest(req *ProxyRequest) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if _, exists := rm.requests[req.ID]; exists {
		return fmt.Errorf("request already exists: %s", req.ID)
	}

	req.Status = StatusPending
	req.RequestTime = time.Now()
	rm.requests[req.ID] = req

	util.Info("New proxy request: id=%s, client=%s, port=%d, type=%s",
		req.ID, req.ClientID, req.LocalPort, req.ProxyType)

	return nil
}

func (rm *RequestManager) ReleasePort(port int) {
	rm.portPool.ReleasePort(port)
}

// ApproveRequest 批准请求
func (rm *RequestManager) ApproveRequest(requestID string) (*ProxyRequest, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	req, exists := rm.requests[requestID]
	if !exists {
		return nil, fmt.Errorf("request not found: %s", requestID)
	}

	if req.Status != StatusPending {
		return nil, fmt.Errorf("request already processed: %s", req.Status)
	}

	// 分配端口
	port, err := rm.portPool.AllocatePort()
	if err != nil {
		return nil, fmt.Errorf("allocate port: %w", err)
	}

	req.Status = StatusApproved
	req.RemotePort = port

	util.Info("Request approved: id=%s, client=%s, remote_port=%d",
		req.ID, req.ClientID, port)

	return req, nil
}

// ApproveRequestWithPort 批准请求并使用指定的端口和名称
func (rm *RequestManager) ApproveRequestWithPort(requestID string, remotePort int, proxyName string) (*ProxyRequest, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	req, exists := rm.requests[requestID]
	if !exists {
		return nil, fmt.Errorf("request not found: %s", requestID)
	}

	if req.Status != StatusPending {
		return nil, fmt.Errorf("request already processed: %s", req.Status)
	}

	// 检查端口是否可用
	if !rm.portPool.IsPortAvailable(remotePort) {
		return nil, fmt.Errorf("port %d is not available", remotePort)
	}

	// 标记端口为已使用
	if err := rm.portPool.MarkPortUsed(remotePort); err != nil {
		return nil, fmt.Errorf("mark port used: %w", err)
	}

	req.Status = StatusApproved
	req.RemotePort = remotePort
	req.ProxyName = proxyName

	util.Info("Request approved: id=%s, client=%s, remote_port=%d, name=%s",
		req.ID, req.ClientID, remotePort, proxyName)

	return req, nil
}

// RejectRequest 拒绝请求
func (rm *RequestManager) RejectRequest(requestID, reason string) (*ProxyRequest, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	req, exists := rm.requests[requestID]
	if !exists {
		return nil, fmt.Errorf("request not found: %s", requestID)
	}

	if req.Status != StatusPending {
		return nil, fmt.Errorf("request already processed: %s", req.Status)
	}

	req.Status = StatusRejected
	req.Reason = reason

	util.Info("Request rejected: id=%s, client=%s, reason=%s",
		req.ID, req.ClientID, reason)

	return req, nil
}

// GetRequest 获取请求
func (rm *RequestManager) GetRequest(requestID string) (*ProxyRequest, bool) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	req, ok := rm.requests[requestID]
	return req, ok
}

// GetPendingRequests 获取所有待审批请求
func (rm *RequestManager) GetPendingRequests() []*ProxyRequest {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	pending := make([]*ProxyRequest, 0)
	for _, req := range rm.requests {
		if req.Status == StatusPending {
			pending = append(pending, req)
		}
	}
	return pending
}

// GetAllRequests 获取所有请求
func (rm *RequestManager) GetAllRequests() []*ProxyRequest {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	all := make([]*ProxyRequest, 0, len(rm.requests))
	for _, req := range rm.requests {
		all = append(all, req)
	}
	return all
}

// RemoveRequest 移除请求
func (rm *RequestManager) RemoveRequest(requestID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if req, exists := rm.requests[requestID]; exists {
		// 如果已分配端口，释放它
		if req.Status == StatusApproved && req.RemotePort > 0 {
			rm.portPool.ReleasePort(req.RemotePort)
		}
		delete(rm.requests, requestID)
	}
}

// cleanupExpired 清理超时请求
func (rm *RequestManager) cleanupExpired() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rm.mu.Lock()
		now := time.Now()
		for id, req := range rm.requests {
			if req.Status == StatusPending && now.Sub(req.RequestTime) > rm.timeout {
				req.Status = StatusTimeout
				util.Info("Request timeout: id=%s, client=%s", id, req.ClientID)
			}

			// 删除超过1小时的已处理请求
			if req.Status != StatusPending && now.Sub(req.RequestTime) > time.Hour {
				if req.Status == StatusApproved && req.RemotePort > 0 {
					rm.portPool.ReleasePort(req.RemotePort)
				}
				delete(rm.requests, id)
			}
		}
		rm.mu.Unlock()
	}
}

// CreateProxyConfigFromRequest 从请求创建代理配置
func CreateProxyConfigFromRequest(req *ProxyRequest) *protocol.ProxyConfig {
	return &protocol.ProxyConfig{
		Name:       req.ProxyName,
		Type:       req.ProxyType,
		LocalIP:    req.LocalIP,
		LocalPort:  req.LocalPort,
		RemotePort: req.RemotePort,
	}
}
