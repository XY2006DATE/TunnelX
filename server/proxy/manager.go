package proxy

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/util"
)

// Manager 代理管理器
type Manager struct {
	proxies             map[string]*TCPProxy     // proxy_name -> tcp_proxy
	udpProxies          map[string]*UDPProxy     // proxy_name -> udp_proxy
	httpProxy           *HTTPProxy               // HTTP虚拟主机代理
	httpsProxy          *HTTPProxy               // HTTPS虚拟主机代理
	pendingConns        map[string]chan net.Conn // connection_id -> work_conn channel
	archivedBytesIn     int64
	archivedBytesOut    int64
	archivedConnections int
	mu                  sync.RWMutex
}

type Info struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	RemotePort       int    `json:"remote_port"`
	BytesIn          int64  `json:"bytes_in"`
	BytesOut         int64  `json:"bytes_out"`
	Connections      int    `json:"connections"`
	TotalConnections int    `json:"total_connections"`
}

// NewManager 创建代理管理器
func NewManager() *Manager {
	return &Manager{
		proxies:      make(map[string]*TCPProxy),
		udpProxies:   make(map[string]*UDPProxy),
		pendingConns: make(map[string]chan net.Conn),
	}
}

// AddProxy 添加代理
func (m *Manager) AddProxy(proxy *TCPProxy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proxies[proxy.GetName()] = proxy
}

// RemoveProxy 移除代理
func (m *Manager) RemoveProxy(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if proxy, ok := m.proxies[name]; ok {
		bytesIn, bytesOut, _, totalConnections := proxy.Stats()
		m.archivedBytesIn += bytesIn
		m.archivedBytesOut += bytesOut
		m.archivedConnections += totalConnections
		proxy.Stop()
		delete(m.proxies, name)
	}
}

// GetProxy 获取代理
func (m *Manager) GetProxy(name string) (*TCPProxy, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	proxy, ok := m.proxies[name]
	return proxy, ok
}

// List returns a snapshot of the currently active listeners.
func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Info, 0, len(m.proxies)+len(m.udpProxies))
	for _, item := range m.proxies {
		bytesIn, bytesOut, connections, totalConnections := item.Stats()
		result = append(result, Info{Name: item.GetName(), Type: "tcp", RemotePort: item.GetRemotePort(), BytesIn: bytesIn, BytesOut: bytesOut, Connections: connections, TotalConnections: totalConnections})
	}
	for _, item := range m.udpProxies {
		result = append(result, Info{Name: item.GetName(), Type: "udp", RemotePort: item.GetRemotePort()})
	}
	return result
}

func (m *Manager) Totals() (bytesIn, bytesOut int64, activeConnections, totalConnections int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bytesIn, bytesOut, totalConnections = m.archivedBytesIn, m.archivedBytesOut, m.archivedConnections
	for _, item := range m.proxies {
		in, out, active, total := item.Stats()
		bytesIn += in
		bytesOut += out
		activeConnections += active
		totalConnections += total
	}
	return
}

// NotifyNewConnection 通知客户端有新连接（实现ProxyManager接口）
func (m *Manager) NotifyNewConnection(proxyName, connectionID string, conn net.Conn) error {
	m.mu.Lock()
	ch := make(chan net.Conn, 1)
	m.pendingConns[connectionID] = ch
	m.mu.Unlock()

	util.Debug("Registered pending connection: %s", connectionID)
	return nil
}

// GetWorkConn 获取工作连接（实现ProxyManager接口）
func (m *Manager) GetWorkConn(connectionID string, timeout time.Duration) (net.Conn, error) {
	m.mu.RLock()
	ch, ok := m.pendingConns[connectionID]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("connection not found: %s", connectionID)
	}

	select {
	case conn := <-ch:
		// 清理
		m.mu.Lock()
		delete(m.pendingConns, connectionID)
		m.mu.Unlock()
		return conn, nil
	case <-time.After(timeout):
		// 超时清理
		m.mu.Lock()
		delete(m.pendingConns, connectionID)
		m.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for work connection")
	}
}

// RegisterWorkConn 注册工作连接
func (m *Manager) RegisterWorkConn(connectionID string, conn net.Conn) error {
	m.mu.RLock()
	ch, ok := m.pendingConns[connectionID]
	m.mu.RUnlock()

	if !ok {
		conn.Close()
		return fmt.Errorf("no pending connection: %s", connectionID)
	}

	select {
	case ch <- conn:
		util.Debug("Work connection registered: %s", connectionID)
		return nil
	default:
		conn.Close()
		return fmt.Errorf("channel full for connection: %s", connectionID)
	}
}

// AddUDPProxy 添加UDP代理
func (m *Manager) AddUDPProxy(proxy *UDPProxy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.udpProxies[proxy.GetName()] = proxy
}

// RemoveUDPProxy 移除UDP代理
func (m *Manager) RemoveUDPProxy(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if proxy, ok := m.udpProxies[name]; ok {
		proxy.Stop()
		delete(m.udpProxies, name)
	}
}

// SetupHTTPProxy 设置HTTP虚拟主机代理
func (m *Manager) SetupHTTPProxy(port int, bindAddr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.httpProxy != nil {
		return nil // 已经设置
	}

	m.httpProxy = NewHTTPProxy(port, m)
	if err := m.httpProxy.Start(bindAddr); err != nil {
		m.httpProxy = nil
		return err
	}

	return nil
}

// AddHTTPVHost 添加HTTP虚拟主机
func (m *Manager) AddHTTPVHost(domain, proxyName string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.httpProxy == nil {
		return fmt.Errorf("HTTP proxy not started")
	}

	m.httpProxy.AddVHost(domain, proxyName)
	return nil
}

// StopAll 停止所有代理
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, proxy := range m.proxies {
		proxy.Stop()
	}
	m.proxies = make(map[string]*TCPProxy)

	for _, proxy := range m.udpProxies {
		proxy.Stop()
	}
	m.udpProxies = make(map[string]*UDPProxy)

	if m.httpProxy != nil {
		m.httpProxy.Stop()
		m.httpProxy = nil
	}

	if m.httpsProxy != nil {
		m.httpsProxy.Stop()
		m.httpsProxy = nil
	}

	// 清理待处理连接
	for _, ch := range m.pendingConns {
		close(ch)
	}
	m.pendingConns = make(map[string]chan net.Conn)
}
