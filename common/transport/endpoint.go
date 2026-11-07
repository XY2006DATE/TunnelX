package transport

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const DefaultWebSocketPath = "/proxy"

type ServerEndpoint struct {
	Address   string // hostname for TCP; complete canonical URL for WebSocket
	Port      int
	TLS       bool
	WebSocket bool
}

func IsWebSocketAddress(address string) bool {
	return strings.HasPrefix(address, "ws://") || strings.HasPrefix(address, "wss://")
}

func ServerHost(address string) string {
	if IsWebSocketAddress(address) {
		if endpoint, err := url.Parse(address); err == nil {
			return endpoint.Hostname()
		}
	}
	return address
}

// HTTP(S) URLs with a non-root path select WebSocket. Root HTTP(S) URLs
// retain the legacy direct TCP/TLS behavior; ws(s) always selects WebSocket.
func ParseServerEndpoint(raw string, fallbackPort int, fallbackTLS bool) (ServerEndpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ServerEndpoint{}, fmt.Errorf("server address is required")
	}
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		if fallbackPort < 1 || fallbackPort > 65535 {
			return ServerEndpoint{}, fmt.Errorf("invalid server port")
		}
		return ServerEndpoint{Address: ip.String(), Port: fallbackPort, TLS: fallbackTLS}, nil
	}
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "server://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Hostname() == "" {
		return ServerEndpoint{}, fmt.Errorf("invalid server address")
	}
	if u.User != nil || u.Fragment != "" {
		return ServerEndpoint{}, fmt.Errorf("server URL must not contain credentials or a fragment")
	}
	scheme := strings.ToLower(u.Scheme)
	tlsEnabled, useWebSocket := fallbackTLS, false
	switch scheme {
	case "ws", "wss":
		useWebSocket = true
		tlsEnabled = scheme == "wss"
	case "http", "https":
		tlsEnabled = scheme == "https"
		useWebSocket = (u.Path != "" && u.Path != "/") || u.RawQuery != ""
	case "tcp", "tls", "server":
		if scheme != "server" {
			tlsEnabled = scheme == "tls"
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return ServerEndpoint{}, fmt.Errorf("TCP/TLS addresses cannot contain a path or query")
		}
	default:
		return ServerEndpoint{}, fmt.Errorf("supported schemes: http, https, ws, wss, tcp, tls")
	}
	port := fallbackPort
	if useWebSocket {
		if tlsEnabled {
			port = 443
		} else {
			port = 80
		}
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return ServerEndpoint{}, fmt.Errorf("invalid server port")
		}
	}
	if port < 1 || port > 65535 {
		return ServerEndpoint{}, fmt.Errorf("server port must be between 1 and 65535")
	}
	host := strings.ToLower(u.Hostname())
	if !useWebSocket {
		return ServerEndpoint{Address: host, Port: port, TLS: tlsEnabled}, nil
	}
	if tlsEnabled {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	if u.Path == "" || u.Path == "/" {
		u.Path = DefaultWebSocketPath
		u.RawPath = ""
	}
	return ServerEndpoint{Address: u.String(), Port: port, TLS: tlsEnabled, WebSocket: true}, nil
}
