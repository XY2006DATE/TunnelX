package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/XY2006DATE/TunnelX/common/auth"
	"github.com/XY2006DATE/TunnelX/common/config"
	"github.com/XY2006DATE/TunnelX/common/transport"
	"github.com/XY2006DATE/TunnelX/common/util"
	"github.com/XY2006DATE/TunnelX/server/control"
	"github.com/XY2006DATE/TunnelX/server/dashboard"
	"github.com/XY2006DATE/TunnelX/server/proxy"
	"github.com/soheilhy/cmux"
)

type Server struct {
	config        *config.ServerConfig
	clientManager *control.Manager
	proxyManager  *proxy.Manager
	dashboard     *dashboard.Dashboard
	tlsConfig     *tls.Config
}

func NewServer(configFile string) (*Server, error) {
	cfg, err := config.LoadServerConfig(configFile)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if err := util.InitLogger(cfg.Log.Level, cfg.Log.File); err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}

	util.Info("Server configuration loaded (bind=%s:%d, tls=%v)",
		cfg.Server.BindAddr, cfg.Server.BindPort, cfg.TLS.Enable)

	srv := &Server{
		config: cfg,
	}

	if cfg.TLS.Enable {
		tlsConfig, err := transport.NewTLSServerConfig(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("init TLS: %w", err)
		}
		srv.tlsConfig = tlsConfig
		util.Info("TLS enabled")
	}

	authenticator := auth.NewAuthenticator(cfg.Auth.Token)

	// 创建代理管理器
	srv.proxyManager = proxy.NewManager()

	// 创建端口池
	portPool := control.NewPortPool(cfg.PortPool.Start, cfg.PortPool.End)
	util.Info("Port pool initialized: %d-%d", cfg.PortPool.Start, cfg.PortPool.End)

	// 创建请求管理器
	requestManager := control.NewRequestManager(portPool, 30*time.Minute)

	// 创建客户端管理器
	srv.clientManager = control.NewManager(
		authenticator,
		time.Duration(cfg.HeartbeatTimeout)*time.Second,
		srv.proxyManager,
		requestManager,
		cfg.Server.BindAddr,
		srv.tlsConfig,
	)

	// 创建Dashboard（如果启用）
	if cfg.Dashboard.Enable {
		srv.dashboard = dashboard.NewDashboard(
			cfg.Server.BindPort,
			cfg.Dashboard.PasswordFile,
			configFile+".stats.json",
			cfg.Auth.Token,
			func(newToken string) error {
				oldToken := cfg.Auth.Token
				cfg.Auth.Token = newToken
				if err := config.SaveServerConfig(configFile, cfg); err != nil {
					cfg.Auth.Token = oldToken
					return err
				}
				authenticator.UpdateToken(newToken)
				util.Info("Connection token updated without interrupting existing client sessions")
				return nil
			},
			func(port int) error {
				oldPort := cfg.Server.BindPort
				cfg.Server.BindPort = port
				cfg.Dashboard.Port = port
				if err := config.SaveServerConfig(configFile, cfg); err != nil {
					cfg.Server.BindPort = oldPort
					cfg.Dashboard.Port = oldPort
					return err
				}
				return nil
			},
			srv.clientManager,
			srv.proxyManager,
		)
		util.Info("Dashboard enabled on shared port %d", cfg.Server.BindPort)
	}

	return srv, nil
}

func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Server.BindAddr, s.config.Server.BindPort)

	var listener net.Listener
	var err error

	if s.config.TLS.Enable {
		listener, err = transport.ListenTLS("tcp", addr, s.tlsConfig)
	} else {
		listener, err = net.Listen("tcp", addr)
	}

	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	util.Info("Server started on %s", addr)

	// 控制协议与 Dashboard 共用同一个 TCP 端口。
	if s.dashboard != nil {
		multiplexer := cmux.New(listener)
		httpListener := multiplexer.Match(cmux.HTTP1Fast())
		controlListener := multiplexer.Match(cmux.Any())
		go func() {
			if err := s.dashboard.Serve(httpListener); err != nil && err != http.ErrServerClosed {
				util.Error("Dashboard error: %v", err)
			}
		}()
		go func() {
			if err := multiplexer.Serve(); err != nil && !strings.Contains(err.Error(), "closed") {
				util.Error("Port multiplexer error: %v", err)
			}
		}()
		listener = controlListener
	}

	go s.handleSignals()

	for {
		conn, err := listener.Accept()
		if err != nil {
			util.Error("Accept error: %v", err)
			continue
		}

		util.Info("New connection from %s", conn.RemoteAddr())
		go s.clientManager.HandleClient(conn)
	}
}

func (s *Server) handleSignals() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	util.Info("Received signal: %v", sig)
	s.Shutdown()
	os.Exit(0)
}

func (s *Server) Shutdown() {
	util.Info("Server shutting down...")
	clients := s.clientManager.GetAllClients()
	for _, client := range clients {
		s.clientManager.RemoveClient(client.ID)
	}
	util.Sync()
	util.Info("Server stopped")
}

func main() {
	configFile := "server.yaml"
	if len(os.Args) > 1 {
		configFile = os.Args[1]
	}

	server, err := NewServer(configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}

	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
