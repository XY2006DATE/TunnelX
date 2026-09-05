package util

import (
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// CompressedConn 压缩连接包装器
type CompressedConn struct {
	conn   net.Conn
	reader io.Reader
	writer io.Writer
	gzr    *gzip.Reader
	gzw    *gzip.Writer
	mu     sync.Mutex
}

// NewCompressedConn 创建压缩连接
func NewCompressedConn(conn net.Conn, level int) (*CompressedConn, error) {
	gzw, err := gzip.NewWriterLevel(conn, level)
	if err != nil {
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}

	return &CompressedConn{
		conn:   conn,
		writer: gzw,
		gzw:    gzw,
	}, nil
}

// Read 读取数据（自动解压）
func (c *CompressedConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 延迟初始化reader（等待第一次读取）
	if c.reader == nil {
		gzr, err := gzip.NewReader(c.conn)
		if err != nil {
			return 0, fmt.Errorf("create gzip reader: %w", err)
		}
		c.gzr = gzr
		c.reader = gzr
	}

	return c.reader.Read(b)
}

// Write 写入数据（自动压缩）
func (c *CompressedConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, err := c.writer.Write(b)
	if err != nil {
		return n, err
	}

	// 刷新缓冲区确保数据发送
	if err := c.gzw.Flush(); err != nil {
		return n, err
	}

	return n, nil
}

// Close 关闭连接
func (c *CompressedConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error

	// 关闭写入器
	if c.gzw != nil {
		if err := c.gzw.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// 关闭读取器
	if c.gzr != nil {
		if err := c.gzr.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// 关闭底层连接
	if err := c.conn.Close(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errs[0]
	}

	return nil
}

// LocalAddr 获取本地地址
func (c *CompressedConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// RemoteAddr 获取远程地址
func (c *CompressedConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

// SetDeadline 设置读写截止时间
func (c *CompressedConn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

// SetReadDeadline 设置读取截止时间
func (c *CompressedConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

// SetWriteDeadline 设置写入截止时间
func (c *CompressedConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}
