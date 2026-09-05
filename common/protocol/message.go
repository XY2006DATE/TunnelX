package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
)

// MessageType 消息类型
type MessageType string

const (
	TypeRegister      MessageType = "register"
	TypeRegisterAck   MessageType = "register_ack"
	TypeHeartbeat     MessageType = "heartbeat"
	TypeHeartbeatAck  MessageType = "heartbeat_ack"
	TypeProxyRequest  MessageType = "proxy_request"
	TypeProxyResponse MessageType = "proxy_response"
	TypeProxyReqNew   MessageType = "proxy_request_new"
	TypeProxyApproval MessageType = "proxy_approval"
	TypeProxyDelete   MessageType = "proxy_delete"
	TypeClientDelete  MessageType = "client_delete"
	TypeClose         MessageType = "close"
	TypeError         MessageType = "error"
)

// Message 通用消息结构
type Message struct {
	Type      MessageType     `json:"type"`
	Timestamp int64           `json:"timestamp,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// RegisterRequest 注册请求
type RegisterRequest struct {
	ClientID string        `json:"client_id"`
	Token    string        `json:"token"`
	Proxies  []ProxyConfig `json:"proxies"`
	Version  string        `json:"version"`
}

// RegisterResponse 注册响应
type RegisterResponse struct {
	Success        bool     `json:"success"`
	Message        string   `json:"message,omitempty"`
	ServerVersion  string   `json:"server_version,omitempty"`
	DeletedProxies []string `json:"deleted_proxies,omitempty"`
}

// ProxyConfig 代理配置
type ProxyConfig struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"` // tcp, udp, http, https
	LocalIP        string   `json:"local_ip"`
	LocalPort      int      `json:"local_port"`
	RemotePort     int      `json:"remote_port,omitempty"`
	CustomDomains  []string `json:"custom_domains,omitempty"`
	UseEncryption  bool     `json:"use_encryption,omitempty"`
	UseCompression bool     `json:"use_compression,omitempty"`
}

// HeartbeatRequest 心跳请求
type HeartbeatRequest struct {
	ClientID string `json:"client_id"`
}

// HeartbeatResponse 心跳响应
type HeartbeatResponse struct {
	Success bool `json:"success"`
}

// ProxyRequest 代理请求
type ProxyRequest struct {
	ProxyName  string `json:"proxy_name"`
	RemoteAddr string `json:"remote_addr,omitempty"`
}

// ProxyResponse 代理响应
type ProxyResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// CloseRequest 关闭请求
type CloseRequest struct {
	Reason string `json:"reason,omitempty"`
}

// ErrorMessage 错误消息
type ErrorMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ProxyRequestMessage 代理请求消息（客户端请求新代理）
type ProxyRequestMessage struct {
	RequestID string `json:"request_id"`
	ClientID  string `json:"client_id"`
	LocalIP   string `json:"local_ip,omitempty"`
	LocalPort int    `json:"local_port"`
	ProxyType string `json:"proxy_type"` // tcp, udp, https (server-side TLS termination)
	ProxyName string `json:"proxy_name,omitempty"`
}

// ProxyApprovalMessage 代理审批消息（服务端批准/拒绝）
type ProxyApprovalMessage struct {
	RequestID  string `json:"request_id"`
	Approved   bool   `json:"approved"`
	RemotePort int    `json:"remote_port,omitempty"`
	ProxyName  string `json:"proxy_name,omitempty"`
	LocalIP    string `json:"local_ip,omitempty"`
	LocalPort  int    `json:"local_port,omitempty"`
	ProxyType  string `json:"proxy_type,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type ProxyDeleteMessage struct {
	ProxyName string `json:"proxy_name"`
}

// ClientDeleteMessage tells a connected client that this server relationship
// and every proxy belonging to it have been deleted by the administrator.
type ClientDeleteMessage struct {
	ProxyNames []string `json:"proxy_names,omitempty"`
}

// SendMessage 发送消息（带长度前缀）
func SendMessage(conn net.Conn, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// 发送长度（4字节大端序）
	length := uint32(len(data))
	if err := binary.Write(conn, binary.BigEndian, length); err != nil {
		return fmt.Errorf("write length: %w", err)
	}

	// 发送数据
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write data: %w", err)
	}

	return nil
}

// RecvMessage 接收消息（带长度前缀）
func RecvMessage(conn net.Conn) (*Message, error) {
	// 读取长度
	var length uint32
	if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}

	// 防止过大的消息
	if length > 10*1024*1024 { // 10MB限制
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	// 读取数据
	data := make([]byte, length)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}

	// 解析消息
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}

// NewMessage 创建消息
func NewMessage(msgType MessageType, data interface{}) (*Message, error) {
	var rawData json.RawMessage
	if data != nil {
		bytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("marshal data: %w", err)
		}
		rawData = bytes
	}

	return &Message{
		Type: msgType,
		Data: rawData,
	}, nil
}

// ParseData 解析消息数据
func (m *Message) ParseData(v interface{}) error {
	if m.Data == nil {
		return fmt.Errorf("no data in message")
	}
	return json.Unmarshal(m.Data, v)
}
