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

// StringOrSlice 接受 YAML 中的单个字符串或字符串列表。
type StringOrSlice []string

// UnmarshalYAML 同时支持 `type: x` 与 `type: [x, y]`。
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	var one string
	if err := value.Decode(&one); err == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := value.Decode(&many); err != nil {
		return err
	}
	*s = many
	return nil
}

// RuleMatch 是事件匹配条件。
type RuleMatch struct {
	Type        StringOrSlice     `yaml:"type"`         // 事件类型(单值或列表)
	Labels      map[string]string `yaml:"labels"`       // 标签需全部匹配
	MinSeverity string            `yaml:"min_severity"` // 最低严重级别
}

// NotifyRule 是一条通知路由规则。
type NotifyRule struct {
	Name     string    `yaml:"name"`
	Match    RuleMatch `yaml:"match"`
	To       []string  `yaml:"to"`       // 接收人(静态名或 ${label} 动态解析)
	Channels []string  `yaml:"channels"` // 通道名；空则用全局默认
	Cooldown Duration  `yaml:"cooldown"` // 同一去重键的冷却时长
}

// NotifyConfig 是通知子系统配置。
type NotifyConfig struct {
	Channels []string `yaml:"channels"` // 默认启用的通知器名
	Detector struct {
		Interval           Duration `yaml:"interval"`
		DiskThreshold      float64  `yaml:"disk_threshold"`       // 0-1，使用率阈值
		DeviceIdleUtil     float64  `yaml:"device_idle_util"`     // 0-1，利用率低于此视为空闲
		DeviceIdleDuration Duration `yaml:"device_idle_duration"` // 持续多久判定空置
	} `yaml:"detector"`
	Rules []NotifyRule `yaml:"rules"` // 为空则使用内置默认规则
}

// SSHNodeConfig 描述一个经 SSH 隧道纳管的节点（容器仅开放 SSH 端口的场景）。
type SSHNodeConfig struct {
	Name         string `yaml:"name"`          // 节点名(仅日志)
	Addr         string `yaml:"addr"`          // 容器 sshd 可达地址 host:port(端口任意)
	User         string `yaml:"user"`          // SSH 用户
	Key          string `yaml:"key"`           // SSH 私钥路径
	KnownHost    string `yaml:"known_host"`    // 主机公钥行；空则跳过校验(不安全)
	RemoteListen string `yaml:"remote_listen"` // 容器内回环监听地址，Agent 拨号此处
}

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
	Web struct {
		HTTP string `yaml:"http"` // Web 控制台 + JSON API 监听地址；空则不启用
	} `yaml:"web"`
	Scheduler struct {
		Interval  Duration `yaml:"interval"`   // 调度循环周期
		Backfill  bool     `yaml:"backfill"`   // EASY backfill 回填(默认开)
		AgeWeight float64  `yaml:"age_weight"` // 排队每分钟增加的优先级(默认 0=关闭)
	} `yaml:"scheduler"`
	Jobs struct {
		LogsDir string `yaml:"logs_dir"` // 作业日志存储目录
	} `yaml:"jobs"`
	Heartbeat struct {
		Timeout      Duration `yaml:"timeout"`       // 超过该时长未心跳判定 DOWN
		ReapInterval Duration `yaml:"reap_interval"` // 巡检失联节点的周期
	} `yaml:"heartbeat"`
	SSHNodes []SSHNodeConfig `yaml:"ssh_nodes"` // 经 SSH 隧道纳管的节点列表
	Notify   NotifyConfig    `yaml:"notify"`    // 事件与通知
	Log      struct {
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
	c.Web.HTTP = ":8080"
	c.Scheduler.Interval = Duration(2 * time.Second)
	c.Scheduler.Backfill = true
	c.Jobs.LogsDir = "job-logs"
	c.Notify.Channels = []string{"log"}
	c.Notify.Detector.Interval = Duration(15 * time.Second)
	c.Notify.Detector.DiskThreshold = 0.9
	c.Notify.Detector.DeviceIdleUtil = 0.05
	c.Notify.Detector.DeviceIdleDuration = Duration(30 * time.Minute)
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
	if v := os.Getenv("SKIPPER_WEB_HTTP"); v != "" {
		c.Web.HTTP = v
	}
	if v := os.Getenv("SKIPPER_JOBS_LOGS_DIR"); v != "" {
		c.Jobs.LogsDir = v
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
	Jobs struct {
		PollInterval Duration `yaml:"poll_interval"` // 拉取作业分配的周期
	} `yaml:"jobs"`
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
	c.Jobs.PollInterval = Duration(2 * time.Second)
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
