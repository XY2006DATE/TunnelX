package proxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XY2006DATE/TunnelX/common/util"
)

// TCPProxy TCP代理
type TCPProxy struct {
	name             string
	proxyType        string
	remotePort       int
	listener         net.Listener
	manager          ProxyManager
	tlsConfig        *tls.Config
	mu               sync.RWMutex
	running          bool
	bytesIn          atomic.Int64
	bytesOut         atomic.Int64
	connections      atomic.Int64
	totalConnections atomic.Int64
}

// ProxyManager 代理管理器接口
type ProxyManager interface {
	NotifyNewConnection(proxyName, connectionID string, conn net.Conn) error
	GetWorkConn(connectionID string, timeout time.Duration) (net.Conn, error)
}

// NewTCPProxy 创建TCP代理
func NewTCPProxy(name string, remotePort int, manager ProxyManager) *TCPProxy {
	return &TCPProxy{
		name:       name,
		proxyType:  "tcp",
		remotePort: remotePort,
		manager:    manager,
		running:    false,
	}
}

// NewHTTPSProxy creates an HTTPS public listener. TLS is terminated on the
// server and the decrypted HTTP stream is forwarded to the client's local
// service.
func NewHTTPSProxy(name string, remotePort int, manager ProxyManager, tlsConfig *tls.Config) (*TCPProxy, error) {
	if tlsConfig == nil || len(tlsConfig.Certificates) == 0 {
		return nil, fmt.Errorf("HTTPS proxy requires server TLS certificate")
	}
	return &TCPProxy{
		name:       name,
		proxyType:  "https",
		remotePort: remotePort,
		manager:    manager,
		tlsConfig:  tlsConfig.Clone(),
		running:    false,
	}, nil
}

// NewHTTPAndHTTPSProxy is kept for compatibility with older callers. New code
// should use NewHTTPSProxy; the public listener is HTTPS-only.
func NewHTTPAndHTTPSProxy(name string, remotePort int, manager ProxyManager, tlsConfig *tls.Config) (*TCPProxy, error) {
	return NewHTTPSProxy(name, remotePort, manager, tlsConfig)
}

// Start 启动TCP代理监听
func (p *TCPProxy) Start(bindAddr string) error {
	addr := fmt.Sprintf("%s:%d", bindAddr, p.remotePort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	p.mu.Lock()
	p.listener = listener
	p.running = true
	p.mu.Unlock()

	util.Info("TCP proxy started: %s on %s", p.name, addr)

	go p.acceptLoop()
	return nil
}

// acceptLoop 接受连接循环
func (p *TCPProxy) acceptLoop() {
	for p.running {
		conn, err := p.listener.Accept()
		if err != nil {
			if p.running {
				util.Error("TCP proxy accept error: %v", err)
			}
			continue
		}

		go p.handleConnection(conn)
	}
}

// handleConnection 处理单个连接
func (p *TCPProxy) handleConnection(userConn net.Conn) {
	p.connections.Add(1)
	p.totalConnections.Add(1)
	defer p.connections.Add(-1)

	forwardConn, isTLS, err := p.prepareUserConnection(userConn)
	if err != nil {
		userConn.Close()
		util.Debug("Failed to prepare public connection: %v", err)
		return
	}
	defer forwardConn.Close()

	connectionID := fmt.Sprintf("%s_%d", p.name, time.Now().UnixNano())
	util.Debug("New connection: %s from %s (tls=%v)", connectionID, userConn.RemoteAddr(), isTLS)

	// 通知客户端有新连接
	if err := p.manager.NotifyNewConnection(p.name, connectionID, forwardConn); err != nil {
		util.Error("Failed to notify new connection: %v", err)
		return
	}

	// 等待客户端建立工作连接（最多10秒）
	workConn, err := p.manager.GetWorkConn(connectionID, 10*time.Second)
	if err != nil {
		util.Error("Failed to get work connection: %v", err)
		return
	}
	defer workConn.Close()

	util.Debug("Work connection established: %s", connectionID)

	// 双向转发数据
	p.forwardData(forwardConn, workConn, connectionID)
}

func (p *TCPProxy) prepareUserConnection(conn net.Conn) (net.Conn, bool, error) {
	if p.tlsConfig == nil {
		return conn, false, nil
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, false, err
	}
	tlsConn := tls.Server(conn, p.tlsConfig.Clone())
	if err := tlsConn.Handshake(); err != nil {
		return nil, true, fmt.Errorf("TLS handshake: %w", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, true, err
	}
	return tlsConn, true, nil
}

// forwardData 双向转发数据
func (p *TCPProxy) forwardData(conn1, conn2 net.Conn, connectionID string) {
	var wg sync.WaitGroup
	wg.Add(2)

	// conn1 -> conn2
	go func() {
		defer wg.Done()
		written, err := io.Copy(conn2, conn1)
		p.bytesIn.Add(written)
		if err != nil {
			util.Debug("Forward %s->%s error: %v", conn1.RemoteAddr(), conn2.RemoteAddr(), err)
		}
		util.Debug("Forward %s->%s: %d bytes", conn1.RemoteAddr(), conn2.RemoteAddr(), written)
		closeWrite(conn2)
	}()

	// conn2 -> conn1
	go func() {
		defer wg.Done()
		written, err := io.Copy(conn1, conn2)
		p.bytesOut.Add(written)
		if err != nil {
			util.Debug("Forward %s->%s error: %v", conn2.RemoteAddr(), conn1.RemoteAddr(), err)
		}
		util.Debug("Forward %s->%s: %d bytes", conn2.RemoteAddr(), conn1.RemoteAddr(), written)
		closeWrite(conn1)
	}()

	wg.Wait()
	util.Info("Connection closed: %s", connectionID)
}

func closeWrite(conn net.Conn) {
	if writer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = writer.CloseWrite()
	}
}

func (p *TCPProxy) Stats() (bytesIn, bytesOut int64, connections, totalConnections int) {
	return p.bytesIn.Load(), p.bytesOut.Load(), int(p.connections.Load()), int(p.totalConnections.Load())
}

// Stop 停止代理
func (p *TCPProxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.running = false
	if p.listener != nil {
		p.listener.Close()
		util.Info("TCP proxy stopped: %s", p.name)
	}
}

// GetName 获取代理名称
func (p *TCPProxy) GetName() string {
	return p.name
}

// GetRemotePort 获取远程端口
func (p *TCPProxy) GetRemotePort() int {
	return p.remotePort
}

func (p *TCPProxy) GetType() string {
	return p.proxyType
}
