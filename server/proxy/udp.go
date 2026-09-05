package proxy

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/util"
)

// UDPProxy UDP代理
type UDPProxy struct {
	name       string
	remotePort int
	conn       *net.UDPConn
	manager    ProxyManager
	sessions   map[string]*udpSession
	mu         sync.RWMutex
	running    bool
}

// udpSession UDP会话
type udpSession struct {
	clientAddr *net.UDPAddr
	workConn   net.Conn
	lastActive time.Time
	mu         sync.Mutex
}

// NewUDPProxy 创建UDP代理
func NewUDPProxy(name string, remotePort int, manager ProxyManager) *UDPProxy {
	return &UDPProxy{
		name:       name,
		remotePort: remotePort,
		manager:    manager,
		sessions:   make(map[string]*udpSession),
		running:    false,
	}
}

// Start 启动UDP代理监听
func (p *UDPProxy) Start(bindAddr string) error {
	addr := fmt.Sprintf("%s:%d", bindAddr, p.remotePort)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve udp addr: %w", err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp on %s: %w", addr, err)
	}

	p.mu.Lock()
	p.conn = conn
	p.running = true
	p.mu.Unlock()

	util.Info("UDP proxy started: %s on %s", p.name, addr)

	go p.receiveLoop()
	go p.cleanupLoop()

	return nil
}

// receiveLoop 接收数据循环
func (p *UDPProxy) receiveLoop() {
	buffer := make([]byte, 65535) // UDP最大包大小

	for p.running {
		n, clientAddr, err := p.conn.ReadFromUDP(buffer)
		if err != nil {
			if p.running {
				util.Error("UDP proxy read error: %v", err)
			}
			continue
		}

		data := make([]byte, n)
		copy(data, buffer[:n])

		go p.handlePacket(clientAddr, data)
	}
}

// handlePacket 处理UDP数据包
func (p *UDPProxy) handlePacket(clientAddr *net.UDPAddr, data []byte) {
	sessionKey := clientAddr.String()

	p.mu.RLock()
	session, exists := p.sessions[sessionKey]
	p.mu.RUnlock()

	if !exists {
		// 创建新会话
		session = p.createSession(clientAddr)
		if session == nil {
			return
		}
	}

	// 更新活跃时间
	session.mu.Lock()
	session.lastActive = time.Now()
	session.mu.Unlock()

	// 发送数据到工作连接
	if _, err := session.workConn.Write(data); err != nil {
		util.Error("Failed to write to work conn: %v", err)
		p.removeSession(sessionKey)
	}
}

// createSession 创建新会话
func (p *UDPProxy) createSession(clientAddr *net.UDPAddr) *udpSession {
	sessionKey := clientAddr.String()
	connectionID := fmt.Sprintf("%s_%d_%s", p.name, time.Now().UnixNano(), sessionKey)

	util.Debug("Creating UDP session: %s from %s", connectionID, clientAddr)

	// 通知客户端有新连接
	if err := p.manager.NotifyNewConnection(p.name, connectionID, nil); err != nil {
		util.Error("Failed to notify new connection: %v", err)
		return nil
	}

	// 等待客户端建立工作连接
	workConn, err := p.manager.GetWorkConn(connectionID, 10*time.Second)
	if err != nil {
		util.Error("Failed to get work connection: %v", err)
		return nil
	}

	session := &udpSession{
		clientAddr: clientAddr,
		workConn:   workConn,
		lastActive: time.Now(),
	}

	p.mu.Lock()
	p.sessions[sessionKey] = session
	p.mu.Unlock()

	util.Debug("UDP session created: %s", sessionKey)

	// 启动从工作连接读取数据
	go p.readFromWorkConn(session, sessionKey)

	return session
}

// readFromWorkConn 从工作连接读取数据
func (p *UDPProxy) readFromWorkConn(session *udpSession, sessionKey string) {
	buffer := make([]byte, 65535)

	for p.running {
		session.workConn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := session.workConn.Read(buffer)
		if err != nil {
			util.Debug("Work conn read error: %v", err)
			p.removeSession(sessionKey)
			return
		}

		// 发送回客户端
		if _, err := p.conn.WriteToUDP(buffer[:n], session.clientAddr); err != nil {
			util.Error("Failed to write to UDP client: %v", err)
			p.removeSession(sessionKey)
			return
		}

		session.mu.Lock()
		session.lastActive = time.Now()
		session.mu.Unlock()
	}
}

// removeSession 移除会话
func (p *UDPProxy) removeSession(sessionKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if session, ok := p.sessions[sessionKey]; ok {
		session.workConn.Close()
		delete(p.sessions, sessionKey)
		util.Debug("UDP session removed: %s", sessionKey)
	}
}

// cleanupLoop 清理过期会话
func (p *UDPProxy) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for p.running {
		<-ticker.C

		p.mu.Lock()
		now := time.Now()
		var toRemove []string

		for key, session := range p.sessions {
			session.mu.Lock()
			if now.Sub(session.lastActive) > 60*time.Second {
				toRemove = append(toRemove, key)
			}
			session.mu.Unlock()
		}

		for _, key := range toRemove {
			if session, ok := p.sessions[key]; ok {
				session.workConn.Close()
				delete(p.sessions, key)
				util.Debug("UDP session timeout: %s", key)
			}
		}
		p.mu.Unlock()
	}
}

// Stop 停止代理
func (p *UDPProxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.running = false

	if p.conn != nil {
		p.conn.Close()
	}

	// 关闭所有会话
	for key, session := range p.sessions {
		session.workConn.Close()
		delete(p.sessions, key)
	}

	util.Info("UDP proxy stopped: %s", p.name)
}

// GetName 获取代理名称
func (p *UDPProxy) GetName() string {
	return p.name
}

// GetRemotePort 获取远程端口
func (p *UDPProxy) GetRemotePort() int {
	return p.remotePort
}
