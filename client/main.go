package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/XY2006DATE/TunnelX/client/control"
	"github.com/XY2006DATE/TunnelX/client/dashboard"
	"github.com/XY2006DATE/TunnelX/client/request"
	"github.com/XY2006DATE/TunnelX/common/config"
	"github.com/XY2006DATE/TunnelX/common/protocol"
	"github.com/XY2006DATE/TunnelX/common/util"
)

type serverSession struct {
	key, addr, token string
	port             int
	controller       *control.Connection
}
type Client struct {
	config                       *config.ClientConfig
	configFile                   string
	configMu                     sync.RWMutex
	sessions                     map[string]*serverSession
	requestSession, proxySession map[string]string
	sessionMu                    sync.RWMutex
	dashboard                    *dashboard.ClientDashboard
	requestManager               *request.Manager
}

func sessionKey(addr string, port int) string { return net.JoinHostPort(addr, strconv.Itoa(port)) }

func canReuseSession(currentToken, submittedToken string, connected bool) bool {
	return connected || currentToken == submittedToken
}

func normalizeServerEndpoint(raw string, fallbackPort int) (string, int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, fmt.Errorf("服务端地址不能为空")
	}
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		if fallbackPort < 1 || fallbackPort > 65535 {
			return "", 0, fmt.Errorf("控制端口必须在 1-65535 之间")
		}
		return ip.String(), fallbackPort, nil
	}
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "tcp://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Hostname() == "" {
		return "", 0, fmt.Errorf("服务端地址格式不正确")
	}
	port := fallbackPort
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return "", 0, fmt.Errorf("控制端口格式不正确")
		}
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("控制端口必须在 1-65535 之间")
	}
	return strings.ToLower(u.Hostname()), port, nil
}

func NewClient(file string) (*Client, error) {
	cfg, err := config.LoadClientConfig(file)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.ClientID == "" {
		cfg.ClientID = fmt.Sprintf("client_%d", time.Now().Unix())
		if err := config.SaveClientConfig(file, cfg); err != nil {
			return nil, err
		}
	}
	if err := util.InitLogger(cfg.Log.Level, cfg.Log.File); err != nil {
		return nil, err
	}
	c := &Client{config: cfg, configFile: file, sessions: map[string]*serverSession{}, requestSession: map[string]string{}, proxySession: map[string]string{}, requestManager: request.NewManager()}
	groups := map[string][]config.ProxyConfig{}
	for i := range cfg.Proxies {
		p := &cfg.Proxies[i]
		if p.ServerAddr == "" {
			p.ServerAddr = cfg.ServerAddr
		}
		if p.ServerPort == 0 {
			p.ServerPort = cfg.ServerPort
		}
		if p.ServerToken == "" {
			p.ServerToken = cfg.Auth.Token
		}
		if p.ServerAddr == "" || p.ServerToken == "" {
			continue
		}
		key := sessionKey(p.ServerAddr, p.ServerPort)
		groups[key] = append(groups[key], *p)
		c.proxySession[key+"|"+p.Name] = key
		id := "restored-" + key + "-" + p.Name
		c.requestManager.RestoreApprovedWithID(id, p.Name, p.LocalIP, p.LocalPort, p.Type, p.RemotePort, p.ServerAddr, p.ServerPort)
	}
	for key, proxies := range groups {
		p := proxies[0]
		c.addSession(key, p.ServerAddr, p.ServerPort, p.ServerToken, proxies)
	}
	for _, pending := range cfg.PendingDeletes {
		if pending.ServerAddr == "" || pending.ServerPort == 0 || pending.ServerToken == "" {
			continue
		}
		key := sessionKey(pending.ServerAddr, pending.ServerPort)
		if c.sessions[key] == nil {
			c.addSession(key, pending.ServerAddr, pending.ServerPort, pending.ServerToken, nil)
		}
	}
	c.requestManager.SetOnProxyApprove(c.setupApprovedProxy)
	c.setupDashboard()
	util.Info("Client configuration loaded (servers=%d, proxies=%d)", len(c.sessions), len(cfg.Proxies))
	return c, nil
}

func (c *Client) sessionConfig(addr string, port int, token string, proxies []config.ProxyConfig) *config.ClientConfig {
	cfg := *c.config
	cfg.ServerAddr = addr
	cfg.ServerPort = port
	cfg.Auth.Token = token
	cfg.Proxies = append([]config.ProxyConfig(nil), proxies...)
	return &cfg
}
func (c *Client) addSession(key, addr string, port int, token string, proxies []config.ProxyConfig) *serverSession {
	s := &serverSession{key: key, addr: addr, port: port, token: token}
	s.controller = control.NewConnection(c.sessionConfig(addr, port, token, proxies))
	s.controller.SetOnMessage(func(m *protocol.Message) { c.handleMessage(s, m) })
	s.controller.SetOnServerSync(func(names []string) {
		for _, name := range names {
			util.Info("Server removed proxy while client was offline: server=%s, proxy=%s", s.key, name)
			c.removePersistedProxy(s.key, name)
			c.requestManager.RemoveProxy(s.addr, s.port, name)
			c.clearPendingDelete(s.key, name)
		}
		c.flushPendingDeletes(s)
		if len(names) > 0 {
			c.removeIdleSession(s)
		}
	})
	c.sessionMu.Lock()
	c.sessions[key] = s
	c.sessionMu.Unlock()
	return s
}
func (c *Client) getOrCreateSession(addr string, port int, token string) (*serverSession, error) {
	key := sessionKey(addr, port)
	c.sessionMu.RLock()
	s := c.sessions[key]
	c.sessionMu.RUnlock()
	if s != nil {
		// An already authenticated control channel remains valid when the server
		// rotates its token. Reuse it without changing the stored credential or
		// interrupting active proxies. A disconnected session still requires the
		// credential it was configured with.
		if !canReuseSession(s.token, token, s.controller.IsConnected()) {
			return nil, fmt.Errorf("该服务端已使用不同的认证令牌连接")
		}
		return s, nil
	}
	return c.addSession(key, addr, port, token, nil), nil
}

func (c *Client) setupDashboard() {
	if !c.config.Dashboard.Enable {
		return
	}
	c.dashboard = dashboard.NewClientDashboard(c.config.Dashboard.Port, c.config.Dashboard.PasswordFile, func(port int) error {
		c.configMu.Lock()
		defer c.configMu.Unlock()
		old := c.config.Dashboard.Port
		c.config.Dashboard.Port = port
		if err := config.SaveClientConfig(c.configFile, c.config); err != nil {
			c.config.Dashboard.Port = old
			return err
		}
		return nil
	})
	c.dashboard.SetOnRequestProxy(func(in dashboard.ProxyRequestInput) error {
		if in.ServerAddr == "" || in.Token == "" {
			return fmt.Errorf("服务端地址、控制端口和认证令牌不能为空")
		}
		serverAddr, serverPort, err := normalizeServerEndpoint(in.ServerAddr, in.ServerPort)
		if err != nil {
			return err
		}
		if in.LocalIP != "127.0.0.1" && in.LocalIP != "localhost" {
			return fmt.Errorf("本地地址仅支持 127.0.0.1 或 localhost")
		}
		s, err := c.getOrCreateSession(serverAddr, serverPort, in.Token)
		if err != nil {
			return err
		}
		if !s.controller.IsConnected() {
			if err := s.controller.Connect(); err != nil {
				return fmt.Errorf("连接服务端失败: %w", err)
			}
		}
		return c.requestProxy(s, in.LocalIP, in.LocalPort, in.ProxyType)
	})
	c.dashboard.SetOnGetRequests(func() interface{} { return c.requestManager.GetAllRequests() })
	c.dashboard.SetOnDeleteProxy(c.deleteProxy)
	go func() {
		if err := c.dashboard.Start("0.0.0.0"); err != nil {
			util.Error("Dashboard failed: %v", err)
		}
	}()
}

func (c *Client) Start() error {
	c.sessionMu.RLock()
	ss := make([]*serverSession, 0, len(c.sessions))
	for _, s := range c.sessions {
		ss = append(ss, s)
	}
	c.sessionMu.RUnlock()
	for _, s := range ss {
		if err := s.controller.Connect(); err != nil {
			util.Error("Restore server %s failed: %v", s.key, err)
		}
	}
	if len(ss) == 0 {
		util.Info("No approved proxies; waiting for a proxy request")
	}
	if c.dashboard != nil {
		go c.updateDashboardStats()
	}
	util.Info("Client started successfully")
	c.handleSignals()
	return nil
}
func (c *Client) handleMessage(s *serverSession, m *protocol.Message) {
	switch m.Type {
	case protocol.TypeProxyApproval:
		var a protocol.ProxyApprovalMessage
		if json.Unmarshal(m.Data, &a) == nil {
			c.sessionMu.Lock()
			c.requestSession[a.RequestID] = s.key
			c.sessionMu.Unlock()
			if err := c.requestManager.HandleApproval(&a); err != nil {
				util.Error("Approval failed: %v", err)
			}
		}
	case protocol.TypeNewProxy:
		var r protocol.NewProxyRequest
		if m.ParseData(&r) == nil {
			go func() {
				if err := s.controller.HandleNewProxy(&r); err != nil {
					util.Error("Work connection failed: %v", err)
				}
			}()
		}
	case protocol.TypeProxyDelete:
		var d protocol.ProxyDeleteMessage
		if m.ParseData(&d) == nil {
			c.removePersistedProxy(s.key, d.ProxyName)
			c.requestManager.RemoveProxy(s.addr, s.port, d.ProxyName)
			c.clearPendingDelete(s.key, d.ProxyName)
			c.removeIdleSession(s)
		}
	case protocol.TypeClientDelete:
		var deleted protocol.ClientDeleteMessage
		if m.ParseData(&deleted) == nil {
			util.Info("Server deleted client configuration: server=%s, proxies=%d", s.key, len(deleted.ProxyNames))
			c.removeServerConfiguration(s)
		}
	}
}
func (c *Client) requestProxy(s *serverSession, localIP string, port int, typ string) error {
	name := fmt.Sprintf("%s-%d", typ, port)
	r, err := c.requestManager.CreateRequest(localIP, port, typ, name)
	if err != nil {
		return err
	}
	_ = c.requestManager.BindServer(r.ID, s.addr, s.port)
	c.sessionMu.Lock()
	c.requestSession[r.ID] = s.key
	c.sessionMu.Unlock()
	return s.controller.SendProxyRequest(r.ID, s.controller.GetClientID(), localIP, port, typ, name)
}
func (c *Client) setupApprovedProxy(r *request.ProxyRequest) error {
	c.sessionMu.RLock()
	key := c.requestSession[r.ID]
	s := c.sessions[key]
	c.sessionMu.RUnlock()
	if s == nil {
		return fmt.Errorf("找不到申请所属的服务端")
	}
	p := config.ProxyConfig{ServerAddr: s.addr, ServerPort: s.port, ServerToken: s.token, Name: r.ProxyName, Type: r.ProxyType, LocalIP: r.LocalIP, LocalPort: r.LocalPort, RemotePort: r.RemotePort}
	s.controller.AddProxy(p)
	c.configMu.Lock()
	updated := false
	for i := range c.config.Proxies {
		q := &c.config.Proxies[i]
		if q.ServerAddr == s.addr && q.ServerPort == s.port && q.Name == p.Name {
			*q = p
			updated = true
			break
		}
	}
	if !updated {
		c.config.Proxies = append(c.config.Proxies, p)
	}
	err := config.SaveClientConfig(c.configFile, c.config)
	c.configMu.Unlock()
	if err != nil {
		return err
	}
	c.sessionMu.Lock()
	c.proxySession[key+"|"+p.Name] = key
	c.sessionMu.Unlock()
	return nil
}
func (c *Client) deleteProxy(name, addr string, port int) error {
	if addr != "" && port > 0 {
		key := sessionKey(addr, port)
		c.sessionMu.RLock()
		s := c.sessions[key]
		_, ok := c.proxySession[key+"|"+name]
		c.sessionMu.RUnlock()
		if s == nil || !ok {
			return fmt.Errorf("找不到指定服务端的代理: %s", name)
		}
		c.queuePendingDelete(s, name)
		c.removePersistedProxy(key, name)
		c.requestManager.RemoveProxy(s.addr, s.port, name)
		if s.controller.IsConnected() {
			if err := s.controller.SendProxyDelete(name); err != nil {
				util.Warn("Proxy deleted locally; server sync deferred: %v", err)
			}
		}
		return nil
	}
	c.sessionMu.RLock()
	var found *serverSession
	for key, s := range c.sessions {
		if _, ok := c.proxySession[key+"|"+name]; ok {
			if found != nil {
				c.sessionMu.RUnlock()
				return fmt.Errorf("多个服务端存在同名代理，请指定服务端")
			}
			found = s
		}
	}
	c.sessionMu.RUnlock()
	if found == nil {
		return fmt.Errorf("找不到代理: %s", name)
	}
	c.queuePendingDelete(found, name)
	c.removePersistedProxy(found.key, name)
	c.requestManager.RemoveProxy(found.addr, found.port, name)
	if found.controller.IsConnected() {
		if err := found.controller.SendProxyDelete(name); err != nil {
			util.Warn("Proxy deleted locally; server sync deferred: %v", err)
		}
	}
	return nil
}

func (c *Client) queuePendingDelete(s *serverSession, name string) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	for _, pending := range c.config.PendingDeletes {
		if sessionKey(pending.ServerAddr, pending.ServerPort) == s.key && pending.Name == name {
			return
		}
	}
	c.config.PendingDeletes = append(c.config.PendingDeletes, config.ProxyDeleteConfig{ServerAddr: s.addr, ServerPort: s.port, ServerToken: s.token, Name: name})
	_ = config.SaveClientConfig(c.configFile, c.config)
}

func (c *Client) clearPendingDelete(key, name string) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	out := c.config.PendingDeletes[:0]
	for _, pending := range c.config.PendingDeletes {
		if !(sessionKey(pending.ServerAddr, pending.ServerPort) == key && pending.Name == name) {
			out = append(out, pending)
		}
	}
	c.config.PendingDeletes = out
	_ = config.SaveClientConfig(c.configFile, c.config)
}

func (c *Client) removeServerConfiguration(s *serverSession) {
	c.configMu.Lock()
	keptProxies := c.config.Proxies[:0]
	for _, proxy := range c.config.Proxies {
		if sessionKey(proxy.ServerAddr, proxy.ServerPort) != s.key {
			keptProxies = append(keptProxies, proxy)
		}
	}
	c.config.Proxies = keptProxies
	keptDeletes := c.config.PendingDeletes[:0]
	for _, pending := range c.config.PendingDeletes {
		if sessionKey(pending.ServerAddr, pending.ServerPort) != s.key {
			keptDeletes = append(keptDeletes, pending)
		}
	}
	c.config.PendingDeletes = keptDeletes
	if err := config.SaveClientConfig(c.configFile, c.config); err != nil {
		util.Error("Failed to save deleted server configuration: %v", err)
	}
	c.configMu.Unlock()

	c.requestManager.RemoveServer(s.addr, s.port)
	c.sessionMu.Lock()
	for key, owner := range c.proxySession {
		if owner == s.key {
			delete(c.proxySession, key)
		}
	}
	for requestID, owner := range c.requestSession {
		if owner == s.key {
			delete(c.requestSession, requestID)
		}
	}
	if c.sessions[s.key] == s {
		delete(c.sessions, s.key)
	}
	c.sessionMu.Unlock()
	s.controller.Close()
}

func (c *Client) flushPendingDeletes(s *serverSession) {
	c.configMu.RLock()
	pending := append([]config.ProxyDeleteConfig(nil), c.config.PendingDeletes...)
	c.configMu.RUnlock()
	for _, item := range pending {
		if sessionKey(item.ServerAddr, item.ServerPort) == s.key {
			if err := s.controller.SendProxyDelete(item.Name); err != nil {
				util.Warn("Deferred proxy deletion sync failed: server=%s, proxy=%s, error=%v", s.key, item.Name, err)
				return
			}
		}
	}
}

func (c *Client) removeIdleSession(s *serverSession) {
	c.configMu.RLock()
	busy := false
	for _, proxy := range c.config.Proxies {
		if sessionKey(proxy.ServerAddr, proxy.ServerPort) == s.key {
			busy = true
			break
		}
	}
	if !busy {
		for _, pending := range c.config.PendingDeletes {
			if sessionKey(pending.ServerAddr, pending.ServerPort) == s.key {
				busy = true
				break
			}
		}
	}
	c.configMu.RUnlock()
	if busy || c.requestManager.HasPendingForServer(s.addr, s.port) {
		return
	}
	c.sessionMu.Lock()
	if c.sessions[s.key] != s {
		c.sessionMu.Unlock()
		return
	}
	delete(c.sessions, s.key)
	c.sessionMu.Unlock()
	util.Info("No proxies remain; disconnecting server session: %s", s.key)
	s.controller.Close()
}
func (c *Client) removePersistedProxy(key, name string) {
	c.sessionMu.RLock()
	s := c.sessions[key]
	c.sessionMu.RUnlock()
	if s != nil {
		s.controller.RemoveProxy(name)
	}
	c.configMu.Lock()
	out := c.config.Proxies[:0]
	for _, p := range c.config.Proxies {
		if !(sessionKey(p.ServerAddr, p.ServerPort) == key && p.Name == name) {
			out = append(out, p)
		}
	}
	c.config.Proxies = out
	_ = config.SaveClientConfig(c.configFile, c.config)
	c.configMu.Unlock()
	c.sessionMu.Lock()
	delete(c.proxySession, key+"|"+name)
	c.sessionMu.Unlock()
}
func (c *Client) updateDashboardStats() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	started := time.Now()
	for range t.C {
		st := &dashboard.ClientStats{Uptime: time.Since(started), ProxyStats: map[string]*dashboard.ProxyStat{}, LastHeartbeat: time.Now()}
		c.sessionMu.RLock()
		ss := make(map[string]*serverSession, len(c.sessions))
		for k, s := range c.sessions {
			ss[k] = s
			if s.controller.IsConnected() {
				st.Connected = true
			}
		}
		c.sessionMu.RUnlock()
		c.configMu.RLock()
		ps := append([]config.ProxyConfig(nil), c.config.Proxies...)
		c.configMu.RUnlock()
		st.ActiveProxies = len(ps)
		for _, p := range ps {
			key := sessionKey(p.ServerAddr, p.ServerPort)
			s := ss[key]
			if s == nil {
				continue
			}
			x := s.controller.TrafficSnapshot(p.Name)
			st.TotalBytesIn += x.BytesIn
			st.TotalBytesOut += x.BytesOut
			st.ProxyStats[key+"|"+p.Name] = &dashboard.ProxyStat{ServerAddr: p.ServerAddr, ServerPort: p.ServerPort, Name: p.Name, Type: p.Type, LocalPort: p.LocalPort, RemotePort: p.RemotePort, BytesIn: x.BytesIn, BytesOut: x.BytesOut, Connections: x.Connections, LastActive: x.LastActive}
		}
		c.dashboard.UpdateStats(st)
	}
}
func (c *Client) handleSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	c.Shutdown()
}
func (c *Client) Shutdown() {
	if c.dashboard != nil {
		c.dashboard.Stop()
	}
	c.sessionMu.RLock()
	for _, s := range c.sessions {
		s.controller.Close()
	}
	c.sessionMu.RUnlock()
	time.Sleep(time.Second)
	util.Sync()
}
func main() {
	file := "client.yaml"
	if len(os.Args) > 1 {
		file = os.Args[1]
	}
	c, err := NewClient(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create client: %v\n", err)
		os.Exit(1)
	}
	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Client error: %v\n", err)
		os.Exit(1)
	}
}
