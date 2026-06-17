// Package transport 实现控制平面到节点的通信隧道。
//
// M3 提供「控制平面发起 SSH + 反向端口转发」的隧道，专为「仅开放 SSH 端口」的容器设计：
// 控制平面 SSH 进容器(SSH 端口任意，不限于 22)，请求 sshd 在容器回环上监听一个端口，
// 把该端口的连接经 SSH 通道转发回控制平面的 gRPC。容器内 Agent 只需拨号该回环端口，
// 所有 gRPC 流量都走在这条 SSH 连接里——容器除 SSH 外无需开放任何端口。
package transport

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config 描述一个 SSH 隧道目标。
type Config struct {
	Name         string // 节点名(仅日志)
	Addr         string // 容器 sshd 的可达地址 host:port(端口任意)
	User         string
	KeyPath      string // SSH 私钥路径
	KnownHost    string // 主机公钥行；为空则跳过校验(不安全)
	RemoteListen string // 容器内回环监听地址，Agent 拨号此处，如 127.0.0.1:7600
}

// SSHTunnel 维护一条到某节点的 SSH 反向转发隧道（断线自动重连）。
type SSHTunnel struct {
	cfg       Config
	forwardTo string // 控制平面本地 gRPC 拨号地址，如 127.0.0.1:7443
	log       *slog.Logger
	ready     chan struct{} // 反向监听首次建立时关闭，供「隧道就绪后再分发 agent」
	readyOnce sync.Once
}

// NewSSHTunnel 创建一条隧道；forwardTo 是控制平面本地 gRPC 地址。
func NewSSHTunnel(cfg Config, forwardTo string, log *slog.Logger) *SSHTunnel {
	return &SSHTunnel{
		cfg: cfg, forwardTo: forwardTo,
		log:   log.With("ssh_node", cfg.Name, "ssh_addr", cfg.Addr),
		ready: make(chan struct{}),
	}
}

// Ready 返回一个在反向监听首次建立时关闭的通道。Agent 经隧道的回环端口回连控制平面，
// 故应在该通道关闭后再分发并拉起 agent，避免首个连接因隧道未就绪而失败。
func (t *SSHTunnel) Ready() <-chan struct{} { return t.ready }

// Run 持续维护隧道，阻塞直到 ctx 取消。
func (t *SSHTunnel) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := t.serveOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			t.log.Warn("ssh tunnel down, will retry", "err", err, "backoff", backoff)
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second // 稳定运行过一段则重置退避
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (t *SSHTunnel) serveOnce(ctx context.Context) error {
	client, err := dialSSH(t.cfg, t.log)
	if err != nil {
		return err
	}
	defer client.Close()

	// 请求 sshd 在容器回环上监听(等价 ssh -R)。
	ln, err := client.Listen("tcp", t.cfg.RemoteListen)
	if err != nil {
		return fmt.Errorf("remote listen %s: %w", t.cfg.RemoteListen, err)
	}
	defer ln.Close()

	t.readyOnce.Do(func() { close(t.ready) })
	t.log.Info("ssh tunnel established", "remote_listen", t.cfg.RemoteListen, "forward_to", t.forwardTo)

	// ctx 取消或保活失败时关闭，使 Accept 返回。
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
		}
		_ = ln.Close()
		_ = client.Close()
	}()
	go t.keepalive(client, stop)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go t.proxy(conn)
	}
}

// proxy 把隧道里来的连接双向转发到控制平面本地 gRPC。
func (t *SSHTunnel) proxy(remote net.Conn) {
	defer remote.Close()
	local, err := net.Dial("tcp", t.forwardTo)
	if err != nil {
		t.log.Warn("dial local grpc failed", "target", t.forwardTo, "err", err)
		return
	}
	defer local.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	<-done
}

// keepalive 周期发送 SSH 保活请求，及时发现失效连接。
func (t *SSHTunnel) keepalive(client *ssh.Client, stop <-chan struct{}) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = client.Close()
				return
			}
		}
	}
}

// dialSSH 按 Config 建立一条 SSH 客户端连接（密钥认证 + 主机指纹校验）。
// 隧道、agent 分发与失联诊断共用此拨号逻辑。
func dialSSH(cfg Config, log *slog.Logger) (*ssh.Client, error) {
	signer, err := loadSigner(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load key: %w", err)
	}
	hostKey, err := hostKeyCallback(cfg.KnownHost, log)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	conf := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKey,
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", cfg.Addr, conf)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	return client, nil
}

func loadSigner(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(b)
}

// hostKeyCallback 按 KnownHost 构造校验回调；为空则跳过校验并告警。
func hostKeyCallback(known string, log *slog.Logger) (ssh.HostKeyCallback, error) {
	if strings.TrimSpace(known) == "" {
		log.Warn("ssh known_host empty; host key verification disabled (insecure)")
		return ssh.InsecureIgnoreHostKey(), nil
	}
	pk, err := parseHostKey(known)
	if err != nil {
		return nil, err
	}
	return ssh.FixedHostKey(pk), nil
}

// parseHostKey 接受 "ssh-ed25519 AAAA..." 或带主机前缀的 known_hosts 行。
func parseHostKey(line string) (ssh.PublicKey, error) {
	if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil {
		return pk, nil
	}
	fields := strings.Fields(line)
	if len(fields) >= 3 {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " "))); err == nil {
			return pk, nil
		}
	}
	return nil, fmt.Errorf("invalid host key %q", line)
}
