package proxy

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/util"
)

const (
	udpIdleTimeout = time.Minute
	maxUDPPeers    = 1024
	udpQueueSize   = 32
)

type UDPProxy struct {
	name       string
	remotePort int
	manager    ProxyManager
	conn       *net.UDPConn
	done       chan struct{}
	sessions   map[string]chan []byte
	mu         sync.Mutex
}

func NewUDPProxy(name string, remotePort int, manager ProxyManager) *UDPProxy {
	return &UDPProxy{name: name, remotePort: remotePort, manager: manager, sessions: make(map[string]chan []byte)}
}

func (p *UDPProxy) Start(bindAddr string) error {
	addr := net.JoinHostPort(bindAddr, strconv.Itoa(p.remotePort))
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve udp addr: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		return fmt.Errorf("UDP proxy is already running")
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp on %s: %w", addr, err)
	}
	p.conn = conn
	p.done = make(chan struct{})
	util.Info("UDP proxy started: %s on %s", p.name, addr)
	go p.receiveLoop(conn, p.done)
	return nil
}

func (p *UDPProxy) receiveLoop(conn *net.UDPConn, done <-chan struct{}) {
	buffer := make([]byte, protocol.MaxDatagramSize)
	for {
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}

		key := addr.String()
		p.mu.Lock()
		if p.conn != conn {
			p.mu.Unlock()
			return
		}
		queue := p.sessions[key]
		if queue == nil && len(p.sessions) < maxUDPPeers {
			queue = make(chan []byte, udpQueueSize)
			p.sessions[key] = queue
			go p.runSession(conn, done, addr, key, queue)
		}
		if queue != nil && len(queue) < cap(queue) {
			queue <- append([]byte(nil), buffer[:n]...)
		}
		p.mu.Unlock()
	}
}

func (p *UDPProxy) runSession(conn *net.UDPConn, done <-chan struct{}, addr *net.UDPAddr, key string, queue chan []byte) {
	defer func() {
		p.mu.Lock()
		if p.sessions[key] == queue {
			delete(p.sessions, key)
		}
		p.mu.Unlock()
	}()

	id := fmt.Sprintf("%s_%d_%s", p.name, time.Now().UnixNano(), key)
	if err := p.manager.NotifyNewConnection(p.name, id, nil); err != nil {
		util.Debug("UDP proxy notification failed: %v", err)
		return
	}
	work, err := p.manager.GetWorkConn(id, 10*time.Second)
	if err != nil {
		util.Debug("UDP work connection failed: %v", err)
		return
	}
	defer work.Close()

	_ = work.SetReadDeadline(time.Now().Add(udpIdleTimeout))
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, protocol.MaxDatagramSize)
		for {
			data, err := protocol.ReadDatagram(work, buffer)
			if err != nil {
				return
			}
			if _, err := conn.WriteToUDP(data, addr); err != nil {
				return
			}
			_ = work.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-readDone:
			return
		case data := <-queue:
			_ = work.SetReadDeadline(time.Now().Add(udpIdleTimeout))
			_ = work.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := protocol.WriteDatagram(work, data); err != nil {
				return
			}
		}
	}
}

func (p *UDPProxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return
	}
	close(p.done)
	_ = p.conn.Close()
	p.conn = nil
	p.sessions = make(map[string]chan []byte)
	util.Info("UDP proxy stopped: %s", p.name)
}

func (p *UDPProxy) GetName() string { return p.name }

func (p *UDPProxy) GetRemotePort() int { return p.remotePort }
