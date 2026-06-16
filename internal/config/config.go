// Package config 负责加载 server / agent 的 YAML 配置，并支持环境变量覆盖。
package config

import (
	"fmt"
	"os"
	"time"

	yaml "gopkg.in/yaml.v3"
)

// Duration 是支持 YAML 字符串("30s")反序列化的 time.Duration 包装。
type Duration time.Duration

// UnmarshalYAML 把 "10s"/"1m" 之类的字符串解析为 Duration。
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Std 返回标准库 time.Duration。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// ServerConfig 是控制平面配置。
type ServerConfig struct {
	Listen struct {
		GRPC string `yaml:"grpc"`
	} `yaml:"listen"`
	Store struct {
		Driver string `yaml:"driver"`
		DSN    string `yaml:"dsn"`
	} `yaml:"store"`
	Metrics struct {
		HTTP string `yaml:"http"` // Prometheus 端点监听地址；空则不启用
	} `yaml:"metrics"`
	Heartbeat struct {
		Timeout      Duration `yaml:"timeout"`       // 超过该时长未心跳判定 DOWN
		ReapInterval Duration `yaml:"reap_interval"` // 巡检失联节点的周期
	} `yaml:"heartbeat"`
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
}

// DefaultServer 返回带默认值的 ServerConfig。
func DefaultServer() ServerConfig {
	var c ServerConfig
	c.Listen.GRPC = ":7443"
	c.Store.Driver = "sqlite"
	c.Store.DSN = "skipper.db"
	c.Metrics.HTTP = ":9100"
	c.Heartbeat.Timeout = Duration(30 * time.Second)
	c.Heartbeat.ReapInterval = Duration(10 * time.Second)
	c.Log.Level = "info"
	return c
}

// LoadServer 加载配置文件(可为空)，再应用环境变量覆盖。
func LoadServer(path string) (ServerConfig, error) {
	c := DefaultServer()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse config: %w", err)
		}
	}
	if v := os.Getenv("SKIPPER_LISTEN_GRPC"); v != "" {
		c.Listen.GRPC = v
	}
	if v := os.Getenv("SKIPPER_STORE_DSN"); v != "" {
		c.Store.DSN = v
	}
	if v := os.Getenv("SKIPPER_METRICS_HTTP"); v != "" {
		c.Metrics.HTTP = v
	}
	if v := os.Getenv("SKIPPER_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	return c, nil
}

// AgentConfig 是节点代理配置。
type AgentConfig struct {
	Server struct {
		Addr string `yaml:"addr"`
	} `yaml:"server"`
	Node struct {
		Name      string            `yaml:"name"` // 为空则取主机名
		Partition string            `yaml:"partition"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"node"`
	Collectors struct {
		Interval Duration `yaml:"interval"`
	} `yaml:"collectors"`
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
}

// DefaultAgent 返回带默认值的 AgentConfig。
func DefaultAgent() AgentConfig {
	var c AgentConfig
	c.Server.Addr = "127.0.0.1:7443"
	c.Node.Partition = "default"
	c.Collectors.Interval = Duration(10 * time.Second)
	c.Log.Level = "info"
	return c
}

// LoadAgent 加载配置文件(可为空)，再应用环境变量覆盖。
func LoadAgent(path string) (AgentConfig, error) {
	c := DefaultAgent()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse config: %w", err)
		}
	}
	if v := os.Getenv("SKIPPER_SERVER_ADDR"); v != "" {
		c.Server.Addr = v
	}
	if v := os.Getenv("SKIPPER_NODE_NAME"); v != "" {
		c.Node.Name = v
	}
	if v := os.Getenv("SKIPPER_NODE_PARTITION"); v != "" {
		c.Node.Partition = v
	}
	if v := os.Getenv("SKIPPER_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	return c, nil
}
