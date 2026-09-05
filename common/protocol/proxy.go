package protocol

const (
	TypeNewProxy      MessageType = "new_proxy"       // 服务端->客户端：有新连接
	TypeNewProxyResp  MessageType = "new_proxy_resp"  // 客户端->服务端：响应
	TypeStartWorkConn MessageType = "start_work_conn" // 客户端->服务端：建立工作连接
)

// ProxyType 代理类型
type ProxyType string

const (
	ProxyTypeTCP   ProxyType = "tcp"
	ProxyTypeUDP   ProxyType = "udp"
	ProxyTypeHTTP  ProxyType = "http"
	ProxyTypeHTTPS ProxyType = "https"
)

// NewProxyRequest 新代理请求（服务端通知客户端有新连接）
type NewProxyRequest struct {
	ProxyName    string `json:"proxy_name"`
	RemoteAddr   string `json:"remote_addr"`
	ConnectionID string `json:"connection_id"` // 连接唯一ID
}

// NewProxyResponse 新代理响应
type NewProxyResponse struct {
	ConnectionID string `json:"connection_id"`
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
}

// StartWorkConn 开始工作连接请求（客户端建立数据连接）
type StartWorkConn struct {
	ConnectionID string `json:"connection_id"`
}
