package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
)

// TLSConfig TLS配置
type TLSConfig struct {
	Enable   bool
	CertFile string
	KeyFile  string
	CAFile   string
}

// NewTLSServerConfig 创建服务端TLS配置
func NewTLSServerConfig(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load certificate: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		},
	}, nil
}

// NewTLSClientConfig 创建客户端TLS配置
func NewTLSClientConfig(caFile string, insecureSkipVerify bool) (*tls.Config, error) {
	config := &tls.Config{
		InsecureSkipVerify: insecureSkipVerify,
		MinVersion:         tls.VersionTLS12,
	}

	if caFile != "" && !insecureSkipVerify {
		caCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}

		config.RootCAs = caCertPool
	}

	return config, nil
}

// WrapTLSServer 包装服务端连接为TLS
func WrapTLSServer(conn net.Conn, config *tls.Config) net.Conn {
	return tls.Server(conn, config)
}

// WrapTLSClient 包装客户端连接为TLS
func WrapTLSClient(conn net.Conn, config *tls.Config, serverName string) net.Conn {
	if serverName != "" {
		config = config.Clone()
		config.ServerName = serverName
	}
	return tls.Client(conn, config)
}

// DialTLS 建立TLS连接
func DialTLS(network, addr string, config *tls.Config) (net.Conn, error) {
	return tls.Dial(network, addr, config)
}

// ListenTLS 监听TLS连接
func ListenTLS(network, addr string, config *tls.Config) (net.Listener, error) {
	return tls.Listen(network, addr, config)
}
