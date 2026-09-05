package util

import (
	"io"
	"sync"
	"time"
)

// BandwidthLimiter 带宽限制器（Token Bucket算法）
type BandwidthLimiter struct {
	rate       int64 // 每秒字节数
	capacity   int64 // 桶容量
	tokens     int64 // 当前令牌数
	lastUpdate time.Time
	mu         sync.Mutex
}

// NewBandwidthLimiter 创建带宽限制器
func NewBandwidthLimiter(bytesPerSecond int64) *BandwidthLimiter {
	capacity := bytesPerSecond * 2 // 桶容量为速率的2倍
	return &BandwidthLimiter{
		rate:       bytesPerSecond,
		capacity:   capacity,
		tokens:     capacity,
		lastUpdate: time.Now(),
	}
}

// Wait 等待获取指定数量的令牌
func (l *BandwidthLimiter) Wait(size int64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 补充令牌
	now := time.Now()
	elapsed := now.Sub(l.lastUpdate)
	l.tokens += int64(float64(l.rate) * elapsed.Seconds())
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
	l.lastUpdate = now

	// 如果令牌足够，直接扣除
	if l.tokens >= size {
		l.tokens -= size
		return 0
	}

	// 令牌不足，计算需要等待的时间
	needed := size - l.tokens
	waitTime := time.Duration(float64(needed)/float64(l.rate)*1e9) * time.Nanosecond
	l.tokens = 0

	return waitTime
}

// Take 尝试获取令牌（非阻塞）
func (l *BandwidthLimiter) Take(size int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 补充令牌
	now := time.Now()
	elapsed := now.Sub(l.lastUpdate)
	l.tokens += int64(float64(l.rate) * elapsed.Seconds())
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
	l.lastUpdate = now

	// 尝试扣除
	if l.tokens >= size {
		l.tokens -= size
		return true
	}

	return false
}

// SetRate 设置速率
func (l *BandwidthLimiter) SetRate(bytesPerSecond int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rate = bytesPerSecond
	l.capacity = bytesPerSecond * 2
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
}

// GetRate 获取当前速率
func (l *BandwidthLimiter) GetRate() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// LimitedWriter 限速写入器
type LimitedWriter struct {
	writer  io.Writer
	limiter *BandwidthLimiter
}

// NewLimitedWriter 创建限速写入器
func NewLimitedWriter(writer io.Writer, limiter *BandwidthLimiter) *LimitedWriter {
	return &LimitedWriter{
		writer:  writer,
		limiter: limiter,
	}
}

// Write 限速写入
func (w *LimitedWriter) Write(p []byte) (int, error) {
	waitTime := w.limiter.Wait(int64(len(p)))
	if waitTime > 0 {
		time.Sleep(waitTime)
	}
	return w.writer.Write(p)
}

// LimitedReader 限速读取器
type LimitedReader struct {
	reader  io.Reader
	limiter *BandwidthLimiter
}

// NewLimitedReader 创建限速读取器
func NewLimitedReader(reader io.Reader, limiter *BandwidthLimiter) *LimitedReader {
	return &LimitedReader{
		reader:  reader,
		limiter: limiter,
	}
}

// Read 限速读取
func (r *LimitedReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		waitTime := r.limiter.Wait(int64(n))
		if waitTime > 0 {
			time.Sleep(waitTime)
		}
	}
	return n, err
}
