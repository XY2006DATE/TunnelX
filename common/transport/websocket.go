package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const WebSocketSubprotocol = "tunnelx.v1"

// WebSocketConn adapts binary WebSocket messages to the existing byte-stream
// protocol. An empty binary message is a write-half-close, so a TCP peer can
// finish a request while continuing to read its response.
type WebSocketConn struct {
	conn                               *websocket.Conn
	readMu, writeMu                    sync.Mutex
	reader                             io.Reader
	frameRead, readClosed, writeClosed bool
	closed                             chan struct{}
	closeOnce                          sync.Once
}

func newWebSocketConn(conn *websocket.Conn) *WebSocketConn {
	conn.SetReadLimit(16 << 20)
	c := &WebSocketConn{conn: conn, closed: make(chan struct{})}
	go c.keepAlive()
	return c
}

func (c *WebSocketConn) keepAlive() {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				c.Close()
				return
			}
		}
	}
}

func (c *WebSocketConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if c.readClosed {
			return 0, io.EOF
		}
		if c.reader == nil {
			kind, reader, err := c.conn.NextReader()
			if err != nil {
				c.Close()
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return 0, io.EOF
				}
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				c.Close()
				return 0, fmt.Errorf("TunnelX requires binary WebSocket messages")
			}
			c.reader, c.frameRead = reader, false
		}
		n, err := c.reader.Read(p)
		if n > 0 {
			c.frameRead = true
		}
		if err == io.EOF {
			c.reader = nil
			if !c.frameRead {
				c.readClosed = true
				return 0, io.EOF
			}
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			c.Close()
		}
		return n, err
	}
}

func (c *WebSocketConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeClosed {
		return 0, net.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := c.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		c.Close()
		return 0, err
	}
	return len(p), nil
}

func (c *WebSocketConn) CloseWrite() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeClosed {
		return nil
	}
	c.writeClosed = true
	err := c.conn.WriteMessage(websocket.BinaryMessage, nil)
	if err != nil {
		c.Close()
	}
	return err
}

func (c *WebSocketConn) Close() error {
	var err error
	c.closeOnce.Do(func() { close(c.closed); err = c.conn.Close() })
	return err
}
func (c *WebSocketConn) LocalAddr() net.Addr               { return c.conn.LocalAddr() }
func (c *WebSocketConn) RemoteAddr() net.Addr              { return c.conn.RemoteAddr() }
func (c *WebSocketConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }
func (c *WebSocketConn) SetWriteDeadline(t time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.SetWriteDeadline(t)
}
func (c *WebSocketConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func DialWebSocket(address string, tlsConfig *tls.Config) (net.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second, TLSClientConfig: tlsConfig,
		Subprotocols:   []string{WebSocketSubprotocol},
		NetDialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	conn, response, err := dialer.DialContext(context.Background(), address, nil)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, fmt.Errorf("WebSocket connect failed (check URL, Upgrade forwarding and certificate): %w", err)
	}
	if conn.Subprotocol() != WebSocketSubprotocol {
		conn.Close()
		return nil, fmt.Errorf("server did not negotiate TunnelX WebSocket transport")
	}
	return newWebSocketConn(conn), nil
}

// WebSocketHandler exposes only the configured path, leaving all other HTTP
// requests to the Dashboard (or a 404 handler). TunnelX's existing registration
// token and work-channel protocol still authenticate connections after upgrade.
func WebSocketHandler(endpoint string, next http.Handler, handle func(net.Conn)) http.Handler {
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{WebSocketSubprotocol},
		// Native clients send no Origin. Reject browser-originated connections.
		CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != endpoint && r.URL.Path != strings.TrimSuffix(endpoint, "/")+"/" {
			next.ServeHTTP(w, r)
			return
		}
		if !websocket.IsWebSocketUpgrade(r) {
			http.Error(w, "TunnelX WebSocket endpoint requires an Upgrade request", http.StatusUpgradeRequired)
			return
		}
		found := false
		for _, subprotocol := range websocket.Subprotocols(r) {
			if subprotocol == WebSocketSubprotocol {
				found = true
			}
		}
		if !found {
			http.Error(w, "TunnelX WebSocket subprotocol required", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		stream := newWebSocketConn(conn)
		// A work connection outlives HandleClient after it is handed to its bridge.
		handle(stream)
	})
}
