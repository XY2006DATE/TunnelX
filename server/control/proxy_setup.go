package control

import (
	"fmt"
	"net"
	"time"

	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/util"
	"github.com/XY2006DATE/TunnelX/server/proxy"
)

// SetupApprovedProxy starts the public listener for a proxy approved at runtime.
func (m *Manager) SetupApprovedProxy(clientID string, proxyConfig *protocol.ProxyConfig) error {
	if proxyConfig == nil {
		return fmt.Errorf("proxy config is required")
	}
	m.mu.Lock()
	if deleted := m.deletedProxies[clientID]; deleted != nil {
		delete(deleted, proxyConfig.Name)
		if len(deleted) == 0 {
			delete(m.deletedProxies, clientID)
		}
	}
	m.mu.Unlock()
	client, err := m.GetClient(clientID)
	if err != nil {
		return err
	}
	client.mu.Lock()
	client.Proxies[proxyConfig.Name] = proxyConfig
	client.mu.Unlock()
	return m.setupProxy(clientID, proxyConfig, m.bindAddr, m.proxyManager)
}

// setupProxies 为客户端设置代理
func (m *Manager) setupProxies(clientID string, proxies map[string]*protocol.ProxyConfig, bindAddr string, proxyMgr *proxy.Manager) {
	// 首先检查是否需要HTTP虚拟主机
	needHTTP := false
	for _, proxyConfig := range proxies {
		if proxyConfig.Type == "http" && len(proxyConfig.CustomDomains) > 0 {
			needHTTP = true
			break
		}
	}

	// 设置HTTP代理（如果需要）
	if needHTTP {
		if err := proxyMgr.SetupHTTPProxy(80, bindAddr); err != nil {
			util.Error("Failed to setup HTTP proxy: %v", err)
		}
	}

	for _, proxyConfig := range proxies {
		if err := m.setupProxy(clientID, proxyConfig, bindAddr, proxyMgr); err != nil {
			util.Error("Failed to start proxy %s: %v", proxyConfig.Name, err)
		}
	}
}

func (m *Manager) setupProxy(clientID string, proxyConfig *protocol.ProxyConfig, bindAddr string, proxyMgr *proxy.Manager) error {
	wrapper := &proxyWrapper{manager: m, proxyMgr: proxyMgr, clientID: clientID}
	var err error
	switch proxyConfig.Type {
	case "tcp":
		// 创建TCP代理
		tcpProxy := proxy.NewTCPProxy(
			proxyConfig.Name,
			proxyConfig.RemotePort,
			wrapper,
		)
		err = tcpProxy.Start(bindAddr)
		if err == nil {
			proxyMgr.AddProxy(tcpProxy)
		}

	case "udp":
		// 创建UDP代理
		udpProxy := proxy.NewUDPProxy(
			proxyConfig.Name,
			proxyConfig.RemotePort,
			wrapper,
		)
		err = udpProxy.Start(bindAddr)
		if err == nil {
			proxyMgr.AddUDPProxy(udpProxy)
		}

	case "http":
		// HTTP虚拟主机
		if len(proxyConfig.CustomDomains) == 0 {
			return fmt.Errorf("HTTP proxy %s has no custom domains", proxyConfig.Name)
		}

		// 添加虚拟主机映射
		for _, domain := range proxyConfig.CustomDomains {
			if err := proxyMgr.AddHTTPVHost(domain, proxyConfig.Name); err != nil {
				util.Error("Failed to add vhost %s: %v", domain, err)
			} else {
				util.Info("HTTP vhost: %s -> %s (client: %s)", domain, proxyConfig.Name, clientID)
			}
		}
		return nil

	default:
		return fmt.Errorf("unsupported proxy type: %s", proxyConfig.Type)
	}

	if err != nil {
		return err
	}

	util.Info("Proxy setup: %s (%s) -> %s:%d (client: %s)",
		proxyConfig.Name, proxyConfig.Type, bindAddr, proxyConfig.RemotePort, clientID)
	return nil
}

// proxyWrapper 代理包装器
type proxyWrapper struct {
	manager  *Manager
	proxyMgr *proxy.Manager
	clientID string
}

func (w *proxyWrapper) NotifyNewConnection(proxyName, connectionID string, conn net.Conn) error {
	// 先在代理管理器中注册
	if err := w.proxyMgr.NotifyNewConnection(proxyName, connectionID, conn); err != nil {
		return err
	}

	// 然后通知客户端
	return w.notifyClient(proxyName, connectionID)
}

func (w *proxyWrapper) GetWorkConn(connectionID string, timeout time.Duration) (net.Conn, error) {
	return w.proxyMgr.GetWorkConn(connectionID, timeout)
}

func (w *proxyWrapper) notifyClient(proxyName, connectionID string) error {
	client, err := w.manager.GetClient(w.clientID)
	if err != nil {
		return err
	}

	// 发送新代理请求
	req := &protocol.NewProxyRequest{
		ProxyName:    proxyName,
		ConnectionID: connectionID,
	}

	msg, _ := protocol.NewMessage(protocol.TypeNewProxy, req)
	if err := w.manager.sendToClient(client, msg); err != nil {
		return fmt.Errorf("send new_proxy: %w", err)
	}

	util.Debug("Notified client %s for new proxy: %s (%s)", w.clientID, proxyName, connectionID)
	return nil
}
