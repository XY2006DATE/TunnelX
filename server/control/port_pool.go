package control

import (
	"fmt"
	"net"
	"sync"
)

// PortPool 端口池管理器
type PortPool struct {
	startPort int
	endPort   int
	allocated map[int]bool // port -> allocated
	mu        sync.RWMutex
}

// NewPortPool 创建端口池
func NewPortPool(start, end int) *PortPool {
	if start <= 0 || end <= 0 || start >= end {
		start = 10000
		end = 20000
	}

	return &PortPool{
		startPort: start,
		endPort:   end,
		allocated: make(map[int]bool),
	}
}

// AllocatePort 分配一个可用端口
func (p *PortPool) AllocatePort() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 从起始端口开始查找可用端口
	for port := p.startPort; port <= p.endPort; port++ {
		if p.allocated[port] {
			continue
		}

		// 尝试监听端口以确认可用
		if p.isPortAvailable(port) {
			p.allocated[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", p.startPort, p.endPort)
}

// ReleasePort 释放端口
func (p *PortPool) ReleasePort(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.allocated, port)
}

// IsAllocated 检查端口是否已分配
func (p *PortPool) IsAllocated(port int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allocated[port]
}

// isPortAvailable 检查端口是否可用
func (p *PortPool) isPortAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// IsPortAvailable 检查端口是否在范围内且未被分配
func (p *PortPool) IsPortAvailable(port int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 检查端口是否在范围内
	if port < p.startPort || port > p.endPort {
		return false
	}

	// 检查是否已分配
	if p.allocated[port] {
		return false
	}

	// 检查端口是否实际可用
	return p.isPortAvailable(port)
}

// MarkPortUsed 标记端口为已使用
func (p *PortPool) MarkPortUsed(port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if port < p.startPort || port > p.endPort {
		return fmt.Errorf("port %d out of range %d-%d", port, p.startPort, p.endPort)
	}

	if p.allocated[port] {
		return fmt.Errorf("port %d already allocated", port)
	}

	p.allocated[port] = true
	return nil
}

// GetAllocatedPorts 获取已分配的端口列表
func (p *PortPool) GetAllocatedPorts() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	ports := make([]int, 0, len(p.allocated))
	for port := range p.allocated {
		ports = append(ports, port)
	}
	return ports
}
