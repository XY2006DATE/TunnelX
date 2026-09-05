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
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/util"
	"github.com/XY2006DATE/TunnelX/server/control"
	"github.com/XY2006DATE/TunnelX/server/proxy"
)

//go:embed web/*
var webFS embed.FS

// Dashboard Web管理界面
type Dashboard struct {
	port            int
	passwordFile    string
	passwordHash    string
	sessionToken    string
	token           string // 连接token
	updateToken     func(string) error
	updatePort      func(int) error
	clientManager   *control.Manager
	proxyManager    *proxy.Manager
	server          *http.Server
	stats           *Stats
	statsFile       string
	baseBytesIn     int64
	baseBytesOut    int64
	baseConnections int
	mu              sync.RWMutex
}

// Stats 统计信息
type Stats struct {
	TotalClients      int                    `json:"total_clients"`
	ActiveConnections int                    `json:"active_connections"`
	TotalConnections  int                    `json:"total_connections"`
	TotalBytesIn      int64                  `json:"total_bytes_in"`
	TotalBytesOut     int64                  `json:"total_bytes_out"`
	Uptime            time.Duration          `json:"uptime"`
	ClientStats       map[string]*ClientStat `json:"client_stats"`
	mu                sync.RWMutex
}

// ClientStat 客户端统计
type ClientStat struct {
	ClientID      string    `json:"client_id"`
	ConnectedAt   time.Time `json:"connected_at"`
	BytesIn       int64     `json:"bytes_in"`
	BytesOut      int64     `json:"bytes_out"`
	ActiveProxies int       `json:"active_proxies"`
}

// NewDashboard 创建Dashboard
func NewDashboard(port int, passwordFile, statsFile, token string, updateToken func(string) error, updatePort func(int) error, clientMgr *control.Manager, proxyMgr *proxy.Manager) *Dashboard {
	d := &Dashboard{
		port:          port,
		passwordFile:  passwordFile,
		statsFile:     statsFile,
		sessionToken:  randomHex(32),
		token:         token,
		updateToken:   updateToken,
		updatePort:    updatePort,
		clientManager: clientMgr,
		proxyManager:  proxyMgr,
		stats: &Stats{
			ClientStats: make(map[string]*ClientStat),
		},
	}
	if data, err := os.ReadFile(passwordFile); err == nil {
		d.passwordHash = strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile(statsFile); err == nil {
		var saved struct {
			BytesIn     int64 `json:"bytes_in"`
			BytesOut    int64 `json:"bytes_out"`
			Connections int   `json:"connections"`
		}
		if json.Unmarshal(data, &saved) == nil {
			d.baseBytesIn, d.baseBytesOut, d.baseConnections = saved.BytesIn, saved.BytesOut, saved.Connections
		}
	}
	return d
}

// Start 启动Dashboard
func (d *Dashboard) Start(bindAddr string) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindAddr, d.port))
	if err != nil {
		return err
	}
	return d.Serve(listener)
}

// Serve runs the dashboard on an existing listener.
func (d *Dashboard) Serve(listener net.Listener) error {
	mux := http.NewServeMux()

	// 首次设置与登录接口不需要现有会话。
	mux.HandleFunc("/api/setup-status", d.handleSetupStatus)
	mux.HandleFunc("/api/setup", d.handleSetup)
	mux.HandleFunc("/login", d.handleLogin)
	mux.HandleFunc("/logout", d.handleLogout)

	// API路由
	mux.HandleFunc("/api/stats", d.authMiddleware(d.handleAPIStats))
	mux.HandleFunc("/api/clients", d.authMiddleware(d.handleAPIClients))
	mux.HandleFunc("/api/proxies", d.authMiddleware(d.handleAPIProxies))
	mux.HandleFunc("/api/client/delete", d.authMiddleware(d.handleAPIDeleteClient))
	mux.HandleFunc("/api/token", d.authMiddleware(d.handleAPIToken))
	mux.HandleFunc("/api/runtime-config", d.authMiddleware(d.handleRuntimeConfig))
	mux.HandleFunc("/api/password", d.authMiddleware(d.handlePasswordChange))
	mux.HandleFunc("/api/pending-requests", d.authMiddleware(d.handleAPIPendingRequests))
	mux.HandleFunc("/api/approve-request", d.authMiddleware(d.handleAPIApproveRequest))
	mux.HandleFunc("/api/reject-request", d.authMiddleware(d.handleAPIRejectRequest))
	mux.HandleFunc("/api/request-proxy", d.handleDirectProxyRequest)
	mux.HandleFunc("/api/delete-proxy", d.authMiddleware(d.handleDeleteProxy))
	mux.HandleFunc("/api/update-proxy", d.authMiddleware(d.handleUpdateProxy))

	// React 编译产物与 API 由同一个 HTTP 端口提供。
	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	d.server = &http.Server{
		Addr:    listener.Addr().String(),
		Handler: mux,
	}

	util.Info("Dashboard started on http://%s", listener.Addr())
	if d.passwordHash == "" {
		util.Info("Dashboard requires first-time password setup")
	}

	go d.updateStats()

	return d.server.Serve(listener)
}

func (d *Dashboard) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var input struct {
			Port int `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Port < 1 || input.Port > 65535 {
			http.Error(w, "Invalid port", http.StatusBadRequest)
			return
		}
		if d.updatePort == nil {
			http.Error(w, "Port update unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := d.updatePort(input.Port); err != nil {
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

// authMiddleware 认证中间件
func (d *Dashboard) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.passwordHash == "" {
			http.Error(w, "Dashboard password has not been configured", http.StatusPreconditionRequired)
			return
		}
		cookie, err := r.Cookie("auth")
		if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(d.sessionToken)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleLogin 登录页面
func (d *Dashboard) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if d.passwordHash == "" {
			http.Error(w, "Password not configured", http.StatusPreconditionRequired)
			return
		}
		if verifyPassword(r.FormValue("password"), d.passwordHash) {
			// 设置认证cookie
			http.SetCookie(w, &http.Cookie{
				Name:     "auth",
				Value:    d.sessionToken,
				Path:     "/",
				MaxAge:   86400, // 24小时
				HttpOnly: true,
			})
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}

		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleLogout 登出
func (d *Dashboard) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   "auth",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// handleAPIStats 统计信息API
func (d *Dashboard) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	d.stats.mu.RLock()
	defer d.stats.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.stats)
}

// handleAPIClients 客户端列表API
func (d *Dashboard) handleAPIClients(w http.ResponseWriter, r *http.Request) {
	clients := d.clientManager.GetClientInfoList()

	result := make([]map[string]interface{}, 0, len(clients))
	for _, client := range clients {
		result = append(result, map[string]interface{}{
			"id":             client.ID,
			"address":        client.Address,
			"connected_at":   client.ConnectedAt,
			"last_heartbeat": client.LastHeartbeat,
			"proxies_count":  client.ProxiesCount,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleAPIProxies 代理列表API
func (d *Dashboard) handleAPIProxies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.clientManager.GetProxyInfoList())
}

func (d *Dashboard) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var input struct {
		Name     string `json:"name"`
		ClientID string `json:"client_id"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	var err error
	if input.ClientID != "" {
		err = d.clientManager.DeleteClientProxy(input.ClientID, input.Name)
	} else {
		err = d.clientManager.DeleteProxy(input.Name)
	}
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (d *Dashboard) handleUpdateProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var input struct {
		Name       string `json:"name"`
		RemotePort int    `json:"remote_port"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	if input.RemotePort < 1 || input.RemotePort > 65535 {
		http.Error(w, "Invalid port", 400)
		return
	}
	if err := d.clientManager.UpdateProxy(input.Name, input.RemotePort); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (d *Dashboard) handleAPIDeleteClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clientID := r.FormValue("client_id")
	if clientID == "" {
		http.Error(w, "Missing client_id", http.StatusBadRequest)
		return
	}
	if err := d.clientManager.DeleteClient(clientID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// updateStats 更新统计信息
func (d *Dashboard) updateStats() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()

	for range ticker.C {
		d.stats.mu.Lock()

		clients := d.clientManager.GetClientInfoList()
		bytesIn, bytesOut, activeConnections, totalConnections := d.proxyManager.Totals()
		d.stats.TotalClients = len(clients)
		d.stats.Uptime = time.Since(startTime)
		d.stats.TotalBytesIn = d.baseBytesIn + bytesIn
		d.stats.TotalBytesOut = d.baseBytesOut + bytesOut
		d.stats.ActiveConnections = activeConnections
		d.stats.TotalConnections = d.baseConnections + totalConnections
		saved, _ := json.Marshal(struct {
			BytesIn     int64 `json:"bytes_in"`
			BytesOut    int64 `json:"bytes_out"`
			Connections int   `json:"connections"`
		}{d.stats.TotalBytesIn, d.stats.TotalBytesOut, d.stats.TotalConnections})
		_ = os.WriteFile(d.statsFile, saved, 0600)
		visible := make(map[string]bool, len(clients))

		// 更新客户端统计
		for _, client := range clients {
			visible[client.ID] = true
			if _, exists := d.stats.ClientStats[client.ID]; !exists {
				d.stats.ClientStats[client.ID] = &ClientStat{
					ClientID:      client.ID,
					ConnectedAt:   client.ConnectedAt,
					ActiveProxies: client.ProxiesCount,
				}
			}
		}
		for clientID := range d.stats.ClientStats {
			if !visible[clientID] {
				delete(d.stats.ClientStats, clientID)
			}
		}

		d.stats.mu.Unlock()
	}
}

func (d *Dashboard) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"configured": d.passwordHash != ""})
}

func (d *Dashboard) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.passwordHash != "" {
		http.Error(w, "Password already configured", http.StatusConflict)
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Password) < 8 {
		http.Error(w, "Password must contain at least 8 characters", http.StatusBadRequest)
		return
	}
	d.passwordHash = hashPassword(input.Password)
	if err := os.WriteFile(d.passwordFile, []byte(d.passwordHash+"\n"), 0600); err != nil {
		d.passwordHash = ""
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (d *Dashboard) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
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
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(parts[1])) == 1
}

// Stop 停止Dashboard
func (d *Dashboard) Stop() error {
	if d.server != nil {
		return d.server.Close()
	}
	return nil
}

// handleAPIToken 获取连接token
func (d *Dashboard) handleAPIToken(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var input struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		input.Token = strings.TrimSpace(input.Token)
		if len(input.Token) < 8 || len(input.Token) > 256 {
			http.Error(w, "Token must be between 8 and 256 characters", http.StatusBadRequest)
			return
		}
		if d.updateToken == nil {
			http.Error(w, "Token update is unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := d.updateToken(input.Token); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		d.mu.Lock()
		d.token = input.Token
		d.mu.Unlock()
	}
	d.mu.RLock()
	token := d.token
	d.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token": token,
	})
}

// handleAPIPendingRequests 获取待审批请求列表
func (d *Dashboard) handleAPIPendingRequests(w http.ResponseWriter, r *http.Request) {
	if d.clientManager == nil {
		http.Error(w, "Client manager not available", http.StatusInternalServerError)
		return
	}

	requestMgr := d.clientManager.GetRequestManager()
	if requestMgr == nil {
		http.Error(w, "Request manager not available", http.StatusInternalServerError)
		return
	}

	requests := requestMgr.GetPendingRequests()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(requests)
}

func (d *Dashboard) handleDirectProxyRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Token     string `json:"token"`
		LocalIP   string `json:"local_ip"`
		LocalPort int    `json:"local_port"`
		ProxyType string `json:"proxy_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if input.Token != d.token {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return
	}
	if input.LocalIP == "" || input.LocalPort < 1 || input.LocalPort > 65535 || (input.ProxyType != "tcp" && input.ProxyType != "udp") {
		http.Error(w, "Invalid local target or proxy type", http.StatusBadRequest)
		return
	}
	clients := d.clientManager.GetAllClients()
	if len(clients) != 1 {
		http.Error(w, "Exactly one client must be connected", http.StatusConflict)
		return
	}
	req := &control.ProxyRequest{ID: fmt.Sprintf("http-%d", time.Now().UnixNano()), ClientID: clients[0].ID, ClientAddr: clients[0].Conn.RemoteAddr().String(), LocalIP: input.LocalIP, LocalPort: input.LocalPort, ProxyType: input.ProxyType, ProxyName: fmt.Sprintf("%s-%d", input.ProxyType, input.LocalPort)}
	if err := d.clientManager.GetRequestManager().AddRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "request_id": req.ID, "status": req.Status})
}

// handleAPIApproveRequest 批准代理请求
func (d *Dashboard) handleAPIApproveRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		RequestID  string `json:"request_id"`
		RemotePort int    `json:"remote_port"`
		ProxyName  string `json:"proxy_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.RemotePort < 0 || req.RemotePort > 65535 {
		http.Error(w, "Remote port must be empty or between 1-65535", http.StatusBadRequest)
		return
	}

	if req.ProxyName == "" {
		http.Error(w, "Proxy name is required", http.StatusBadRequest)
		return
	}

	requestMgr := d.clientManager.GetRequestManager()
	if requestMgr == nil {
		http.Error(w, "Request manager not available", http.StatusInternalServerError)
		return
	}

	// 未填写远程端口时从端口池自动分配；填写时使用管理员指定端口。
	var proxyReq *control.ProxyRequest
	var err error
	if req.RemotePort == 0 {
		proxyReq, err = requestMgr.ApproveRequest(req.RequestID)
		if err == nil {
			proxyReq.ProxyName = req.ProxyName
		}
	} else {
		proxyReq, err = requestMgr.ApproveRequestWithPort(req.RequestID, req.RemotePort, req.ProxyName)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.clientManager.SetupApprovedProxy(proxyReq.ClientID, &protocol.ProxyConfig{
		Name: proxyReq.ProxyName, Type: proxyReq.ProxyType, LocalPort: proxyReq.LocalPort, RemotePort: proxyReq.RemotePort,
	}); err != nil {
		http.Error(w, "Failed to start proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 发送审批消息给客户端
	approval := &protocol.ProxyApprovalMessage{
		RequestID:  proxyReq.ID,
		Approved:   true,
		RemotePort: proxyReq.RemotePort,
		ProxyName:  proxyReq.ProxyName,
		LocalIP:    proxyReq.LocalIP,
		LocalPort:  proxyReq.LocalPort,
		ProxyType:  proxyReq.ProxyType,
	}

	if err := d.clientManager.SendProxyApproval(proxyReq.ClientID, approval); err != nil {
		util.Error("Failed to send approval: %v", err)
		http.Error(w, "Failed to send approval to client", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"remote_port": proxyReq.RemotePort,
		"proxy_name":  proxyReq.ProxyName,
	})
}

// handleAPIRejectRequest 拒绝代理请求
func (d *Dashboard) handleAPIRejectRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Reason == "" {
		req.Reason = "Rejected by administrator"
	}

	requestMgr := d.clientManager.GetRequestManager()
	if requestMgr == nil {
		http.Error(w, "Request manager not available", http.StatusInternalServerError)
		return
	}

	// 拒绝请求
	proxyReq, err := requestMgr.RejectRequest(req.RequestID, req.Reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 发送审批消息给客户端
	approval := &protocol.ProxyApprovalMessage{
		RequestID: proxyReq.ID,
		Approved:  false,
		Reason:    proxyReq.Reason,
	}

	if err := d.clientManager.SendProxyApproval(proxyReq.ClientID, approval); err != nil {
		util.Error("Failed to send rejection: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}
