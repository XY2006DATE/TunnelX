package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/util"
)

// HTTPProxy HTTP虚拟主机代理
type HTTPProxy struct {
	port     int
	listener net.Listener
	manager  ProxyManager
	vhosts   map[string]string // domain -> proxy_name
	mu       sync.RWMutex
	running  bool
}

// NewHTTPProxy 创建HTTP代理
func NewHTTPProxy(port int, manager ProxyManager) *HTTPProxy {
	return &HTTPProxy{
		port:    port,
		manager: manager,
		vhosts:  make(map[string]string),
		running: false,
	}
}

// AddVHost 添加虚拟主机
func (p *HTTPProxy) AddVHost(domain, proxyName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vhosts[domain] = proxyName
	util.Info("Virtual host added: %s -> %s", domain, proxyName)
}

// RemoveVHost 移除虚拟主机
func (p *HTTPProxy) RemoveVHost(domain string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.vhosts, domain)
	util.Info("Virtual host removed: %s", domain)
}

// Start 启动HTTP代理
func (p *HTTPProxy) Start(bindAddr string) error {
	addr := fmt.Sprintf("%s:%d", bindAddr, p.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	p.mu.Lock()
	p.listener = listener
	p.running = true
	p.mu.Unlock()

	util.Info("HTTP virtual host proxy started on %s", addr)

	go p.acceptLoop()
	return nil
}

// acceptLoop 接受连接循环
func (p *HTTPProxy) acceptLoop() {
	for p.running {
		conn, err := p.listener.Accept()
		if err != nil {
			if p.running {
				util.Error("HTTP proxy accept error: %v", err)
			}
			continue
		}

		go p.handleConnection(conn)
	}
}

// handleConnection 处理HTTP连接
func (p *HTTPProxy) handleConnection(conn net.Conn) {
	defer conn.Close()

	// 读取HTTP请求头
	reader := bufio.NewReader(conn)

	// 使用超时避免阻塞
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// 读取第一行（请求行）
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		util.Debug("Failed to read request line: %v", err)
		return
	}

	// 读取请求头
	var headers []string
	headers = append(headers, requestLine)

	var host string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			util.Debug("Failed to read header: %v", err)
			return
		}

		headers = append(headers, line)

		// 提取Host头
		if strings.HasPrefix(strings.ToLower(line), "host:") {
			host = strings.TrimSpace(line[5:])
			// 移除端口号
			if idx := strings.Index(host, ":"); idx > 0 {
				host = host[:idx]
			}
		}

		// 空行表示头部结束
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	if host == "" {
		util.Debug("No Host header found")
		p.sendError(conn, 400, "Bad Request: No Host header")
		return
	}

	util.Debug("HTTP request for host: %s", host)

	// 查找虚拟主机
	p.mu.RLock()
	proxyName, ok := p.vhosts[host]
	p.mu.RUnlock()

	if !ok {
		util.Debug("No virtual host configured for: %s", host)
		p.sendError(conn, 404, "Not Found: No virtual host configured")
		return
	}

	// 生成连接ID
	connectionID := fmt.Sprintf("http_%s_%d", proxyName, time.Now().UnixNano())

	// 通知客户端有新连接
	if err := p.manager.NotifyNewConnection(proxyName, connectionID, conn); err != nil {
		util.Error("Failed to notify new connection: %v", err)
		p.sendError(conn, 502, "Bad Gateway")
		return
	}

	// 等待工作连接
	workConn, err := p.manager.GetWorkConn(connectionID, 10*time.Second)
	if err != nil {
		util.Error("Failed to get work connection: %v", err)
		p.sendError(conn, 504, "Gateway Timeout")
		return
	}
	defer workConn.Close()

	// 清除读取超时
	conn.SetReadDeadline(time.Time{})

	// 发送已缓冲的请求头到工作连接
	for _, header := range headers {
		if _, err := workConn.Write([]byte(header)); err != nil {
			util.Error("Failed to write headers: %v", err)
			return
		}
	}

	// 如果还有请求体，继续转发
	util.Debug("HTTP connection established: %s for %s", connectionID, host)

	// 双向转发数据
	p.forwardData(conn, workConn, reader, connectionID)
}

// forwardData 双向转发数据
func (p *HTTPProxy) forwardData(conn net.Conn, workConn net.Conn, reader *bufio.Reader, connectionID string) {
	var wg sync.WaitGroup
	wg.Add(2)

	// 客户端 -> 工作连接
	go func() {
		defer wg.Done()
		// 先转发reader中剩余的数据
		if reader.Buffered() > 0 {
			buf := make([]byte, reader.Buffered())
			reader.Read(buf)
			workConn.Write(buf)
		}
		// 继续转发
		written, _ := io.Copy(workConn, conn)
		util.Debug("HTTP client->work: %d bytes (%s)", written, connectionID)
		workConn.Close()
	}()

	// 工作连接 -> 客户端
	go func() {
		defer wg.Done()
		written, _ := io.Copy(conn, workConn)
		util.Debug("HTTP work->client: %d bytes (%s)", written, connectionID)
		conn.Close()
	}()

	wg.Wait()
	util.Info("HTTP connection closed: %s", connectionID)
}

// sendError 发送HTTP错误响应
func (p *HTTPProxy) sendError(conn net.Conn, code int, message string) {
	response := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s\n",
		code, http.StatusText(code), message)
	conn.Write([]byte(response))
}

// Stop 停止代理
func (p *HTTPProxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.running = false
	if p.listener != nil {
		p.listener.Close()
		util.Info("HTTP virtual host proxy stopped")
	}
}

// GetPort 获取端口
func (p *HTTPProxy) GetPort() int {
	return p.port
}
