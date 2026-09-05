package dashboard

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/util"
)

//go:embed web/*
var webFS embed.FS

// ClientDashboard 客户端Dashboard
type ClientDashboard struct {
	port           int
	passwordFile   string
	passwordHash   string
	sessionToken   string
	enabled        bool
	stats          *ClientStats
	server         *http.Server
	onRequestProxy func(ProxyRequestInput) error
	onDeleteProxy  func(string, string, int) error
	onGetRequests  func() interface{} // 回调函数：获取请求列表
	onUpdatePort   func(int) error
	mu             sync.RWMutex
}

type ProxyRequestInput struct {
	ServerAddr string `json:"server_addr"`
	ServerPort int    `json:"server_port"`
	ServerTLS  *bool  `json:"server_tls"`
	Token      string `json:"token"`
	LocalIP    string `json:"local_ip"`
	LocalPort  int    `json:"local_port"`
	ProxyType  string `json:"proxy_type"`
}

// ClientStats 客户端统计
type ClientStats struct {
	Connected      bool                  `json:"connected"`
	ServerAddr     string                `json:"server_addr"`
	Uptime         time.Duration         `json:"uptime"`
	TotalBytesIn   int64                 `json:"total_bytes_in"`
	TotalBytesOut  int64                 `json:"total_bytes_out"`
	ActiveProxies  int                   `json:"active_proxies"`
	ProxyStats     map[string]*ProxyStat `json:"proxy_stats"`
	LastHeartbeat  time.Time             `json:"last_heartbeat"`
	ReconnectCount int                   `json:"reconnect_count"`
	mu             sync.RWMutex
}

// ProxyStat 代理统计
type ProxyStat struct {
	ServerAddr  string    `json:"server_addr"`
	ServerPort  int       `json:"server_port"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	LocalPort   int       `json:"local_port"`
	RemotePort  int       `json:"remote_port"`
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
	Connections int       `json:"connections"`
	LastActive  time.Time `json:"last_active"`
}

// NewClientDashboard 创建客户端Dashboard
func NewClientDashboard(port int, passwordFile string, onUpdatePort func(int) error) *ClientDashboard {
	d := &ClientDashboard{
		port:         port,
		passwordFile: passwordFile,
		sessionToken: randomHex(32),
		enabled:      true,
		onUpdatePort: onUpdatePort,
		stats: &ClientStats{
			ProxyStats: make(map[string]*ProxyStat),
		},
	}
	if data, err := os.ReadFile(passwordFile); err == nil {
		d.passwordHash = strings.TrimSpace(string(data))
	}
	return d
}

// Start 启动客户端Dashboard
func (d *ClientDashboard) Start(bindAddr string) error {
	mux := http.NewServeMux()

	// React 编译产物与 API 使用同一个 Dashboard 端口。
	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	// API
	mux.HandleFunc("/api/stats", d.authMiddleware(d.handleAPIStats))
	mux.HandleFunc("/api/proxies", d.authMiddleware(d.handleAPIProxies))
	mux.HandleFunc("/api/reconnect", d.authMiddleware(d.handleAPIReconnect))
	mux.HandleFunc("/api/request-proxy", d.authMiddleware(d.handleAPIRequestProxy))
	mux.HandleFunc("/api/delete-proxy", d.authMiddleware(d.handleAPIDeleteProxy))
	mux.HandleFunc("/api/requests", d.authMiddleware(d.handleAPIRequests))
	mux.HandleFunc("/api/runtime-config", d.authMiddleware(d.handleRuntimeConfig))
	mux.HandleFunc("/api/setup-status", d.handleSetupStatus)
	mux.HandleFunc("/api/setup", d.handleSetup)
	mux.HandleFunc("/login", d.handleLogin)
	mux.HandleFunc("/api/password", d.authMiddleware(d.handlePasswordChange))

	addr := fmt.Sprintf("%s:%d", bindAddr, d.port)
	d.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	util.Info("Client Dashboard started on http://%s", addr)

	return d.server.ListenAndServe()
}

func (d *ClientDashboard) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.RLock()
		configured, session := d.passwordHash != "", d.sessionToken
		d.mu.RUnlock()
		if !configured {
			http.Error(w, "Dashboard password has not been configured", http.StatusPreconditionRequired)
			return
		}
		cookie, err := r.Cookie("auth")
		if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(session)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (d *ClientDashboard) handleSetupStatus(w http.ResponseWriter, _ *http.Request) {
	d.mu.RLock()
	configured := d.passwordHash != ""
	d.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"configured": configured})
}

func (d *ClientDashboard) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Password) < 8 {
		http.Error(w, "Password must contain at least 8 characters", http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.passwordHash != "" {
		http.Error(w, "Password already configured", http.StatusConflict)
		return
	}
	hash := hashPassword(input.Password)
	if err := os.WriteFile(d.passwordFile, []byte(hash+"\n"), 0600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.passwordHash = hash
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (d *ClientDashboard) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	d.mu.RLock()
	valid, session := verifyPassword(input.Password, d.passwordHash), d.sessionToken
	d.mu.RUnlock()
	if !valid {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "auth", Value: session, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (d *ClientDashboard) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.NewPassword) < 8 {
		http.Error(w, "New password must contain at least 8 characters", http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !verifyPassword(input.CurrentPassword, d.passwordHash) {
		http.Error(w, "Current password is incorrect", http.StatusUnauthorized)
		return
	}
	hash := hashPassword(input.NewPassword)
	if err := os.WriteFile(d.passwordFile, []byte(hash+"\n"), 0600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.passwordHash = hash
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func randomHex(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func hashPassword(password string) string {
	salt := randomHex(16)
	sum := sha256.Sum256([]byte(salt + password))
	for i := 0; i < 120000; i++ {
		sum = sha256.Sum256(sum[:])
	}
	return salt + ":" + hex.EncodeToString(sum[:])
}

func verifyPassword(password, encoded string) bool {
	parts := strings.SplitN(encoded, ":", 2)
	if len(parts) != 2 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + password))
	for i := 0; i < 120000; i++ {
		sum = sha256.Sum256(sum[:])
	}
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(parts[1])) == 1
}

func (d *ClientDashboard) handleAPIDeleteProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Name       string `json:"name"`
		ServerAddr string `json:"server_addr"`
		ServerPort int    `json:"server_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Name == "" {
		http.Error(w, "Proxy name is required", http.StatusBadRequest)
		return
	}
	if d.onDeleteProxy == nil {
		http.Error(w, "Proxy deletion unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := d.onDeleteProxy(input.Name, input.ServerAddr, input.ServerPort); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (d *ClientDashboard) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var input struct {
			Port int `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Port < 1 || input.Port > 65535 {
			http.Error(w, "Invalid port", http.StatusBadRequest)
			return
		}
		if d.onUpdatePort == nil {
			http.Error(w, "Port update unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := d.onUpdatePort(input.Port); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d.mu.Lock()
		d.port = input.Port
		d.mu.Unlock()
	}
	d.mu.RLock()
	port := d.port
	d.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"port": port})
}

// handleAPIStats 统计API
func (d *ClientDashboard) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	d.stats.mu.RLock()
	defer d.stats.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.stats)
}

// handleAPIProxies 代理列表API
func (d *ClientDashboard) handleAPIProxies(w http.ResponseWriter, r *http.Request) {
	d.stats.mu.RLock()
	defer d.stats.mu.RUnlock()

	proxies := make([]*ProxyStat, 0, len(d.stats.ProxyStats))
	for _, p := range d.stats.ProxyStats {
		proxies = append(proxies, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(proxies)
}

// handleAPIReconnect 重连API
func (d *ClientDashboard) handleAPIReconnect(w http.ResponseWriter, r *http.Request) {
	// TODO: 触发客户端重连

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"message": "Reconnecting...",
	})
}

// handleAPIRequestProxy 请求新代理
func (d *ClientDashboard) handleAPIRequestProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ProxyRequestInput

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.LocalPort <= 0 || req.LocalPort > 65535 {
		http.Error(w, "Invalid port", http.StatusBadRequest)
		return
	}
	if req.ServerAddr == "" || req.ServerPort <= 0 || req.ServerPort > 65535 || req.Token == "" || req.LocalIP == "" {
		http.Error(w, "Server address, control port, token and local address are required", http.StatusBadRequest)
		return
	}

	if req.ProxyType != "tcp" && req.ProxyType != "udp" && req.ProxyType != "https" {
		http.Error(w, "Invalid proxy type", http.StatusBadRequest)
		return
	}

	// 调用回调函数提交请求
	if d.onRequestProxy != nil {
		if err := d.onRequestProxy(req); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Proxy request submitted",
	})
}

// handleAPIRequests 获取请求列表
func (d *ClientDashboard) handleAPIRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if d.onGetRequests != nil {
		requests := d.onGetRequests()
		json.NewEncoder(w).Encode(requests)
	} else {
		json.NewEncoder(w).Encode([]interface{}{})
	}
}

// SetOnRequestProxy 设置请求代理回调
func (d *ClientDashboard) SetOnRequestProxy(callback func(ProxyRequestInput) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onRequestProxy = callback
}

func (d *ClientDashboard) SetOnDeleteProxy(callback func(string, string, int) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onDeleteProxy = callback
}

// SetOnGetRequests 设置获取请求列表回调
func (d *ClientDashboard) SetOnGetRequests(callback func() interface{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onGetRequests = callback
}

// UpdateStats 更新统计信息
func (d *ClientDashboard) UpdateStats(stats *ClientStats) {
	d.stats.mu.Lock()
	defer d.stats.mu.Unlock()

	d.stats.Connected = stats.Connected
	d.stats.ServerAddr = stats.ServerAddr
	d.stats.Uptime = stats.Uptime
	d.stats.TotalBytesIn = stats.TotalBytesIn
	d.stats.TotalBytesOut = stats.TotalBytesOut
	d.stats.ActiveProxies = stats.ActiveProxies
	d.stats.LastHeartbeat = stats.LastHeartbeat
	d.stats.ReconnectCount = stats.ReconnectCount
	d.stats.ProxyStats = stats.ProxyStats
}

// Stop 停止Dashboard
func (d *ClientDashboard) Stop() error {
	if d.server != nil {
		return d.server.Close()
	}
	return nil
}
