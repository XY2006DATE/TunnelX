package config

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ServerConfig 服务端配置
type ServerConfig struct {
	Server struct {
		BindAddr string `yaml:"bind_addr" json:"bind_addr"`
		BindPort int    `yaml:"bind_port" json:"bind_port"`
	} `yaml:"server" json:"server"`

	Auth struct {
		Method string `yaml:"method" json:"method"`
		Token  string `yaml:"token" json:"token"`
	} `yaml:"auth" json:"auth"`

	TLS struct {
		Enable   bool   `yaml:"enable" json:"enable"`
		CertFile string `yaml:"cert_file" json:"cert_file"`
		KeyFile  string `yaml:"key_file" json:"key_file"`
	} `yaml:"tls" json:"tls"`

	Log struct {
		Level      string `yaml:"level" json:"level"`
		File       string `yaml:"file" json:"file"`
		MaxSize    int    `yaml:"max_size" json:"max_size"`
		MaxBackups int    `yaml:"max_backups" json:"max_backups"`
	} `yaml:"log" json:"log"`

	Dashboard struct {
		Enable       bool   `yaml:"enable" json:"enable"`
		Port         int    `yaml:"port" json:"port"`
		PasswordFile string `yaml:"password_file" json:"password_file"`
	} `yaml:"dashboard" json:"dashboard"`

	PortPool struct {
		Start int `yaml:"start" json:"start"`
		End   int `yaml:"end" json:"end"`
	} `yaml:"port_pool" json:"port_pool"`

	HeartbeatTimeout int `yaml:"heartbeat_timeout" json:"heartbeat_timeout"`
}

func LoadServerConfig(configFile string) (*ServerConfig, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var config ServerConfig

	// 尝试YAML
	if err := yaml.Unmarshal(data, &config); err != nil {
		// 尝试JSON
		if jsonErr := json.Unmarshal(data, &config); jsonErr != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	// 设置默认值
	if config.Server.BindAddr == "" {
		config.Server.BindAddr = "0.0.0.0"
	}
	if config.Server.BindPort == 0 {
		config.Server.BindPort = 7000
	}
	if config.Log.Level == "" {
		config.Log.Level = "info"
	}
	if config.HeartbeatTimeout == 0 {
		config.HeartbeatTimeout = 90
	}
	if config.Dashboard.Port == 0 {
		config.Dashboard.Port = 7500
	}
	if config.Dashboard.PasswordFile == "" {
		config.Dashboard.PasswordFile = "dashboard.password"
	}
	if config.PortPool.Start == 0 {
		config.PortPool.Start = 10000
	}
	if config.PortPool.End == 0 {
		config.PortPool.End = 20000
	}

	// 验证
	if config.Server.BindPort <= 0 || config.Server.BindPort > 65535 {
		return nil, fmt.Errorf("invalid bind_port: %d", config.Server.BindPort)
	}

	if config.TLS.Enable {
		if config.TLS.CertFile == "" || config.TLS.KeyFile == "" {
			return nil, fmt.Errorf("tls enabled but cert/key not specified")
		}
	}

	return &config, nil
}

// SaveServerConfig persists the server configuration atomically enough for
// dashboard-managed settings such as the connection token.
func SaveServerConfig(configFile string, config *ServerConfig) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(configFile, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
