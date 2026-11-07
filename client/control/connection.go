package control

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XY2006DATE/TunnelX/common/config"
	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/transport"
	"github.com/XY2006DATE/TunnelX/common/util"
	"github.com/google/uuid"
)

type Connection struct {
	config        *config.ClientConfig
	conn          net.Conn
	connected     bool
	udpFramed     bool
	reconnecting  bool
	closing       bool
	heartbeatStop chan struct{}
	clientID      string
	onMessage     func(*protocol.Message) // 消息回调
	onServerSync  func([]string)
	mu            sync.RWMutex
	sendMu        sync.Mutex
	trafficMu     sync.RWMutex
	traffic       map[string]*ProxyTraffic
}

type ProxyTraffic struct {
	BytesIn, BytesOut, Connections atomic.Int64
	LastActive                     atomic.Int64
}

type ProxyTrafficSnapshot struct {
	BytesIn, BytesOut int64
	Connections       int
	LastActive        time.Time
}

func NewConnection(config *config.ClientConfig) *Connection {
	clientID := config.ClientID
	if clientID == "" {
		clientID = "client_" + uuid.NewString()
	}
	return &Connection{
		config:   config,
		clientID: clientID,
		traffic:  make(map[string]*ProxyTraffic),
	}
}

func (c *Connection) trafficFor(name string) *ProxyTraffic {
	c.trafficMu.RLock()
	t := c.traffic[name]
	c.trafficMu.RUnlock()
	if t != nil {
		return t
	}
	c.trafficMu.Lock()
	defer c.trafficMu.Unlock()
	if c.traffic[name] == nil {
		c.traffic[name] = &ProxyTraffic{}
	}
	return c.traffic[name]
}

func (c *Connection) TrafficSnapshot(name string) ProxyTrafficSnapshot {
	t := c.trafficFor(name)
	last := t.LastActive.Load()
	s := ProxyTrafficSnapshot{BytesIn: t.BytesIn.Load(), BytesOut: t.BytesOut.Load(), Connections: int(t.Connections.Load())}
	if last > 0 {
		s.LastActive = time.Unix(0, last)
	}
	return s
}

func (c *Connection) Connect() error {
	c.mu.Lock()
	if c.connected || c.closing {
		c.mu.Unlock()
		return nil
	}

	addr := net.JoinHostPort(c.config.ServerAddr, strconv.Itoa(c.config.ServerPort))
	if transport.IsWebSocketAddress(c.config.ServerAddr) {
		addr = c.config.ServerAddr
	}
	util.Info("Connecting to server: %s", addr)

	conn, err := c.dialServer()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("connect: %w", err)
	}

	c.conn = conn
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	c.connected = true
	util.Info("Connected to server")

	resp, err := c.register()
	if err != nil {
		c.conn.Close()
		c.conn = nil
		c.connected = false
		c.mu.Unlock()
		return fmt.Errorf("register: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	c.udpFramed = resp.UDPFramed
	stop := make(chan struct{})
	c.heartbeatStop = stop
	onServerSync := c.onServerSync
	c.mu.Unlock()
	go c.startHeartbeat(conn, stop)
	go c.readLoop(conn)
	if onServerSync != nil {
		onServerSync(resp.DeletedProxies)
	}
	return nil
}

// Configure sets the server used for an on-demand proxy request.
func (c *Connection) Configure(serverAddr string, serverPort int, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected {
		if c.config.ServerAddr != serverAddr || c.config.ServerPort != serverPort || c.config.Auth.Token != token {
			return fmt.Errorf("already connected to another server")
		}
		return nil
	}
	c.config.ServerAddr = serverAddr
	c.config.ServerPort = serverPort
	c.config.Auth.Token = token
	return nil
}

func (c *Connection) AddProxy(proxy config.ProxyConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.config.Proxies {
		if c.config.Proxies[i].Name == proxy.Name {
			c.config.Proxies[i] = proxy
			return
		}
	}
	c.config.Proxies = append(c.config.Proxies, proxy)
}

func (c *Connection) RemoveProxy(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	filtered := c.config.Proxies[:0]
	for _, proxy := range c.config.Proxies {
		if proxy.Name != name {
			filtered = append(filtered, proxy)
		}
	}
	c.config.Proxies = filtered
}

func (c *Connection) dialServer() (net.Conn, error) {
	endpoint, err := transport.ParseServerEndpoint(c.config.ServerAddr, c.config.ServerPort, c.config.TLS.Enable)
	if err != nil {
		return nil, err
	}
	if endpoint.WebSocket {
		var tlsConfig *tls.Config
		if endpoint.TLS {
			tlsConfig, err = transport.NewTLSClientConfig(c.config.TLS.CAFile, false)
			if err != nil {
				return nil, err
			}
		}
		return transport.DialWebSocket(endpoint.Address, tlsConfig)
	}
	addr := net.JoinHostPort(c.config.ServerAddr, strconv.Itoa(c.config.ServerPort))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil || !c.config.TLS.Enable {
		return conn, err
	}

	// An empty CA file intentionally uses the operating system trust store.
	// Never disable certificate validation merely because a custom CA was not set.
	tlsConfig, err := transport.NewTLSClientConfig(c.config.TLS.CAFile, false)
	if err != nil {
		conn.Close()
		return nil, err
	}
	tlsConfig.ServerName = c.config.ServerAddr
	tlsConn := tls.Client(conn, tlsConfig)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return tlsConn, nil
}

func (c *Connection) register() (*protocol.RegisterResponse, error) {
	proxies := make([]protocol.ProxyConfig, 0, len(c.config.Proxies))
	for _, p := range c.config.Proxies {
		proxies = append(proxies, protocol.ProxyConfig{
			Name:           p.Name,
			Type:           p.Type,
			LocalIP:        p.LocalIP,
			LocalPort:      p.LocalPort,
			RemotePort:     p.RemotePort,
			CustomDomains:  p.CustomDomains,
			UseEncryption:  p.UseEncryption,
			UseCompression: p.UseCompression,
		})
	}

	req := &protocol.RegisterRequest{
		ClientID: c.clientID,
		Token:    c.config.Auth.Token,
		Proxies:  proxies,
		Version:  "0.2.0",
	}

	msg, _ := protocol.NewMessage(protocol.TypeRegister, req)
	if err := protocol.SendMessage(c.conn, msg); err != nil {
		return nil, err
	}

	util.Info("Register request sent: %s", c.clientID)

	respMsg, err := protocol.RecvMessage(c.conn)
	if err != nil {
		return nil, err
	}

	if respMsg.Type != protocol.TypeRegisterAck {
		return nil, fmt.Errorf("unexpected response type: %s", respMsg.Type)
	}

	var resp protocol.RegisterResponse
	if err := respMsg.ParseData(&resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("register failed: %s", resp.Message)
	}

	util.Info("Registered successfully: %s (server v%s)", resp.Message, resp.ServerVersion)
	return &resp, nil
}

func (c *Connection) startHeartbeat(conn net.Conn, stop <-chan struct{}) {
	interval := time.Duration(c.config.Heartbeat.Interval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	util.Info("Heartbeat started (interval=%v)", interval)

	for {
		select {
		case <-ticker.C:
			req := &protocol.HeartbeatRequest{ClientID: c.clientID}
			msg, _ := protocol.NewMessage(protocol.TypeHeartbeat, req)
			if err := c.sendOnConnection(conn, msg); err != nil {
				util.Error("Heartbeat failed: %v", err)
				c.handleDisconnect(conn)
				return
			}
		case <-stop:
			util.Info("Heartbeat stopped")
			return
		}
	}
}

func (c *Connection) readLoop(conn net.Conn) {
	timeout := time.Duration(c.config.Heartbeat.Timeout) * time.Second
	if timeout <= time.Duration(c.config.Heartbeat.Interval)*time.Second {
		timeout = 3 * time.Duration(c.config.Heartbeat.Interval) * time.Second
	}
	for {
		if timeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(timeout))
		}
		msg, err := protocol.RecvMessage(conn)
		if err != nil {
			c.mu.RLock()
			current := c.conn == conn
			c.mu.RUnlock()
			if current {
				util.Error("Control connection read failed: %v", err)
				c.handleDisconnect(conn)
			}
			return
		}

		if msg.Type == protocol.TypeHeartbeatAck {
			util.Debug("Heartbeat response received")
			continue
		}
		c.mu.RLock()
		callback := c.onMessage
		c.mu.RUnlock()
		if callback != nil {
			callback(msg)
		}
	}
}

func (c *Connection) sendMessage(msg *protocol.Message) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("connection not established")
	}
	return c.sendOnConnection(conn, msg)
}

func (c *Connection) sendOnConnection(conn net.Conn, msg *protocol.Message) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	return protocol.SendMessage(conn, msg)
}

// SendProxyDelete asks the server to remove an approved proxy and release its public port.
func (c *Connection) SendProxyDelete(proxyName string) error {
	msg, err := protocol.NewMessage(protocol.TypeProxyDelete, &protocol.ProxyDeleteMessage{ProxyName: proxyName})
	if err != nil {
		return err
	}
	return c.sendMessage(msg)
}

// HandleNewProxy creates a one-shot work connection and bridges it to the local service.
func (c *Connection) HandleNewProxy(req *protocol.NewProxyRequest) error {
	c.mu.RLock()
	proxyConfig := c.config.GetProxyByName(req.ProxyName)
	if proxyConfig == nil {
		c.mu.RUnlock()
		return fmt.Errorf("proxy not configured: %s", req.ProxyName)
	}
	localAddr := net.JoinHostPort(proxyConfig.LocalIP, strconv.Itoa(proxyConfig.LocalPort))
	network := "tcp"
	if proxyConfig.Type == "udp" {
		network = "udp"
	}
	c.mu.RUnlock()
	if network == "udp" && !req.UDPFramed {
		return fmt.Errorf("UDP forwarding requires a TunnelX 0.2.0 or newer server")
	}
	localConn, err := net.DialTimeout(network, localAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect local service %s: %w", localAddr, err)
	}

	workConn, err := c.dialServer()
	if err != nil {
		localConn.Close()
		return fmt.Errorf("connect work channel: %w", err)
	}
	start, _ := protocol.NewMessage(protocol.TypeStartWorkConn, &protocol.StartWorkConn{ConnectionID: req.ConnectionID})
	if err := protocol.SendMessage(workConn, start); err != nil {
		localConn.Close()
		workConn.Close()
		return fmt.Errorf("start work channel: %w", err)
	}

	traffic := c.trafficFor(req.ProxyName)
	traffic.Connections.Add(1)
	traffic.LastActive.Store(time.Now().UnixNano())
	if network == "udp" {
		go bridgeDatagrams(localConn, workConn, traffic)
	} else {
		go bridgeConnections(localConn, workConn, traffic)
	}
	return nil
}

func bridgeDatagrams(local, work net.Conn, traffic *ProxyTraffic) {
	defer local.Close()
	defer work.Close()
	go func() {
		defer work.Close()
		buffer := make([]byte, protocol.MaxDatagramSize)
		for {
			n, err := local.Read(buffer)
			if err != nil {
				return
			}
			_ = work.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := protocol.WriteDatagram(work, buffer[:n]); err != nil {
				return
			}
			traffic.BytesOut.Add(int64(n))
			traffic.LastActive.Store(time.Now().UnixNano())
		}
	}()
	buffer := make([]byte, protocol.MaxDatagramSize)
	for {
		data, err := protocol.ReadDatagram(work, buffer)
		if err != nil {
			return
		}
		if _, err := local.Write(data); err != nil {
			return
		}
		traffic.BytesIn.Add(int64(len(data)))
		traffic.LastActive.Store(time.Now().UnixNano())
	}
}

func bridgeConnections(a, b net.Conn, traffic *ProxyTraffic) {
	defer a.Close()
	defer b.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(a, b)
		traffic.BytesIn.Add(n)
		traffic.LastActive.Store(time.Now().UnixNano())
		closeWrite(a)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(b, a)
		traffic.BytesOut.Add(n)
		traffic.LastActive.Store(time.Now().UnixNano())
		closeWrite(b)
	}()
	wg.Wait()
}

func closeWrite(conn net.Conn) {
	if writer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = writer.CloseWrite()
	}
}

func (c *Connection) handleDisconnect(conn net.Conn) {
	c.mu.Lock()
	if c.closing || c.conn != conn {
		c.mu.Unlock()
		return
	}
	c.connected = false
	close(c.heartbeatStop)
	c.heartbeatStop = nil
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	util.Warn("Connection lost, attempting to reconnect...")
	go c.ConnectWithRetry()
}

// ConnectWithRetry restores an approved session even if its server is offline at startup.
func (c *Connection) ConnectWithRetry() {
	c.mu.Lock()
	if c.reconnecting {
		c.mu.Unlock()
		return
	}
	c.reconnecting = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		disconnected := !c.connected && !c.closing
		c.mu.Unlock()
		if disconnected {
			go c.ConnectWithRetry()
		}
	}()

	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		c.mu.RLock()
		closing := c.closing
		c.mu.RUnlock()
		if closing {
			return
		}
		util.Info("Reconnecting... (backoff=%v)", backoff)

		if err := c.Connect(); err != nil {
			util.Error("Reconnect failed: %v", err)

			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		util.Info("Reconnected successfully")
		return
	}
}

func (c *Connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closing = true
	if c.heartbeatStop != nil {
		close(c.heartbeatStop)
		c.heartbeatStop = nil
	}

	if c.conn != nil {
		req := &protocol.CloseRequest{
			Reason: "client shutdown",
		}
		msg, _ := protocol.NewMessage(protocol.TypeClose, req)
		_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
		protocol.SendMessage(c.conn, msg)

		c.conn.Close()
		c.conn = nil
	}

	c.connected = false
}

// SendProxyRequest 发送代理请求到服务端
func (c *Connection) SendProxyRequest(requestID, clientID, localIP string, localPort int, proxyType, proxyName string) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("connection not established")
	}

	req := &protocol.ProxyRequestMessage{
		RequestID: requestID,
		ClientID:  clientID,
		LocalIP:   localIP,
		LocalPort: localPort,
		ProxyType: proxyType,
		ProxyName: proxyName,
	}

	msg, err := protocol.NewMessage(protocol.TypeProxyReqNew, req)
	if err != nil {
		return fmt.Errorf("create message: %w", err)
	}

	if err := c.sendMessage(msg); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	util.Info("Proxy request sent: id=%s, port=%d, type=%s", requestID, localPort, proxyType)
	return nil
}

// GetClientID 获取客户端ID
func (c *Connection) GetClientID() string {
	return c.clientID
}

// GetConnection 获取底层连接
func (c *Connection) GetConnection() net.Conn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn
}

// SetOnMessage 设置消息回调
func (c *Connection) SetOnMessage(callback func(*protocol.Message)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMessage = callback
}

// SetOnServerSync handles proxies that the server deleted while this client was offline.
func (c *Connection) SetOnServerSync(callback func([]string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onServerSync = callback
}

func (c *Connection) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

func (c *Connection) SupportsUDP() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected && c.udpFramed
}
