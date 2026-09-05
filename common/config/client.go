package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ClientConfig struct {
	ClientID   string `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	ServerAddr string `yaml:"server_addr,omitempty" json:"server_addr,omitempty"`
	ServerPort int    `yaml:"server_port,omitempty" json:"server_port,omitempty"`

	Auth struct {
		Token string `yaml:"token" json:"token"`
	} `yaml:"auth,omitempty" json:"auth,omitempty"`

	TLS struct {
		Enable bool   `yaml:"enable" json:"enable"`
		CAFile string `yaml:"ca_file" json:"ca_file"`
	} `yaml:"tls" json:"tls"`

	Log struct {
		Level string `yaml:"level" json:"level"`
		File  string `yaml:"file" json:"file"`
	} `yaml:"log" json:"log"`

	Heartbeat struct {
		Interval int `yaml:"interval" json:"interval"`
		Timeout  int `yaml:"timeout" json:"timeout"`
	} `yaml:"heartbeat" json:"heartbeat"`

	Dashboard struct {
		Enable       bool   `yaml:"enable" json:"enable"`
		Port         int    `yaml:"port" json:"port"`
		PasswordFile string `yaml:"password_file" json:"password_file"`
	} `yaml:"dashboard" json:"dashboard"`

	Proxies        []ProxyConfig       `yaml:"proxies" json:"proxies"`
	PendingDeletes []ProxyDeleteConfig `yaml:"pending_deletes,omitempty" json:"pending_deletes,omitempty"`
}

type ProxyDeleteConfig struct {
	ServerAddr  string `yaml:"server_addr" json:"server_addr"`
	ServerPort  int    `yaml:"server_port" json:"server_port"`
	ServerToken string `yaml:"server_token" json:"-"`
	Name        string `yaml:"name" json:"name"`
}

type ProxyConfig struct {
	ServerAddr     string   `yaml:"server_addr,omitempty" json:"server_addr,omitempty"`
	ServerPort     int      `yaml:"server_port,omitempty" json:"server_port,omitempty"`
	ServerToken    string   `yaml:"server_token,omitempty" json:"-"`
	Name           string   `yaml:"name" json:"name"`
	Type           string   `yaml:"type" json:"type"`
	LocalIP        string   `yaml:"local_ip" json:"local_ip"`
	LocalPort      int      `yaml:"local_port" json:"local_port"`
	RemotePort     int      `yaml:"remote_port" json:"remote_port"`
	CustomDomains  []string `yaml:"custom_domains" json:"custom_domains"`
	UseEncryption  bool     `yaml:"use_encryption" json:"use_encryption"`
	UseCompression bool     `yaml:"use_compression" json:"use_compression"`
}

func LoadClientConfig(configFile string) (*ClientConfig, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			config := &ClientConfig{}
			applyClientDefaults(config)
			config.Dashboard.Enable = true
			dir := filepath.Dir(configFile)
			if err := os.MkdirAll(dir, 0750); err != nil {
				return nil, fmt.Errorf("create config directory: %w", err)
			}
			if err := SaveClientConfig(configFile, config); err != nil {
				return nil, fmt.Errorf("create default config: %w", err)
			}
			return config, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var config ClientConfig

	// 尝试YAML
	if err := yaml.Unmarshal(data, &config); err != nil {
		// 尝试JSON
		if jsonErr := json.Unmarshal(data, &config); jsonErr != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	applyClientDefaults(&config)

	// 验证
	for i := range config.Proxies {
		if config.Proxies[i].Name == "" {
			return nil, fmt.Errorf("proxy[%d]: name required", i)
		}
		if config.Proxies[i].LocalIP == "" {
			config.Proxies[i].LocalIP = "127.0.0.1"
		}
		if config.Proxies[i].LocalPort <= 0 {
			return nil, fmt.Errorf("proxy[%s]: invalid local_port", config.Proxies[i].Name)
		}
	}

	return &config, nil
}

func applyClientDefaults(config *ClientConfig) {
	// The root server fields are retained only for legacy configurations. A new
	// client intentionally has no default server connection.
	if config.ServerAddr != "" && config.ServerPort == 0 {
		config.ServerPort = 7100
	}
	if config.Log.Level == "" {
		config.Log.Level = "info"
	}
	if config.Heartbeat.Interval == 0 {
		config.Heartbeat.Interval = 30
	}
	if config.Heartbeat.Timeout == 0 {
		config.Heartbeat.Timeout = 90
	}
	if config.Dashboard.Port == 0 {
		config.Dashboard.Port = 7101
	}
	if config.Dashboard.PasswordFile == "" {
		config.Dashboard.PasswordFile = "dashboard.password"
	}
}

func SaveClientConfig(configFile string, config *ClientConfig) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(configFile, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (c *ClientConfig) GetProxyByName(name string) *ProxyConfig {
	for i := range c.Proxies {
		if c.Proxies[i].Name == name {
			return &c.Proxies[i]
		}
	}
	return nil
}
