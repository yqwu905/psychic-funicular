package transport

// 本文件实现节点失联后的基本诊断：控制平面经 SSH 探测，区分
// 「SSH 连接中断 / agent 进程被杀 / 其他原因」三类，供上层上报为事件并据此自愈。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/crypto/ssh"
)

// DiagCause 是节点失联的诊断结论。
type DiagCause string

const (
	// DiagSSHDown 表示连 SSH 都连不上：网络不可达 / sshd 挂掉 / 认证或主机指纹失败。
	DiagSSHDown DiagCause = "ssh-down"
	// DiagAgentKilled 表示 SSH 正常但 agent 进程不在：被杀 / 崩溃 / OOM。
	DiagAgentKilled DiagCause = "agent-killed"
	// DiagOther 表示 SSH 与 agent 进程都正常但仍无心跳：疑似隧道 / 回环 / 卡死等。
	DiagOther DiagCause = "other"
)

// Diagnosis 是一次诊断的结论与细节。
type Diagnosis struct {
	Cause  DiagCause
	Detail string
}

// Diagnose 在节点失联后做基本诊断：先试 SSH 连接，连得上再查 agent 进程是否存活，
// 据此区分「SSH 连接中断 / agent 进程被杀 / 其他原因」。
func Diagnose(ctx context.Context, cfg Config, pidPath, agentPath string, log *slog.Logger) Diagnosis {
	if err := ctx.Err(); err != nil {
		return Diagnosis{Cause: DiagOther, Detail: "诊断被取消: " + err.Error()}
	}
	client, err := dialSSH(cfg, log)
	if err != nil {
		return classify(err, nil, false)
	}
	defer client.Close()

	alive, perr := agentAlive(client, pidPath, agentPath)
	return classify(nil, perr, alive)
}

// classify 由 SSH 拨号错误、进程探测错误与进程存活情况推断结论（纯函数，便于测试）。
func classify(dialErr, procErr error, alive bool) Diagnosis {
	switch {
	case dialErr != nil:
		return Diagnosis{Cause: DiagSSHDown, Detail: "SSH 连接失败: " + dialErr.Error()}
	case procErr != nil:
		return Diagnosis{Cause: DiagOther, Detail: "SSH 正常，但无法确认 agent 进程状态: " + procErr.Error()}
	case !alive:
		return Diagnosis{Cause: DiagAgentKilled, Detail: "SSH 正常，但 agent 进程未在运行（可能被杀 / 崩溃 / OOM）"}
	default:
		return Diagnosis{Cause: DiagOther, Detail: "SSH 与 agent 进程均正常，但控制平面仍未收到心跳（疑似隧道或回环异常）"}
	}
}

// agentAlive 经 SSH 判断 agent 进程是否存活：优先用拉起时写的 pid 文件 + kill -0，
// 失败再回退扫描 /proc 按二进制路径匹配，兼容手动启动(无 pid 文件)的场景。
func agentAlive(client *ssh.Client, pidPath, agentPath string) (bool, error) {
	sess, err := client.NewSession()
	if err != nil {
		return false, err
	}
	defer sess.Close()
	out, err := sess.Output(aliveCommand(pidPath, agentPath))
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "ALIVE"), nil
}

// aliveCommand 构造判断进程存活的远程命令（纯函数，便于测试）：
// 先读 pid 文件用 kill -0 探测，再回退扫 /proc 按 agent 路径匹配；
// 只用 cat/kill/tr/grep 等普遍存在的工具，避免依赖 pgrep/ps。
func aliveCommand(pidPath, agentPath string) string {
	return fmt.Sprintf(`p=$(cat %s 2>/dev/null); if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then echo ALIVE; exit 0; fi; for c in /proc/[0-9]*/cmdline; do if tr '\0' ' ' < "$c" 2>/dev/null | grep -qF %s; then echo ALIVE; exit 0; fi; done; echo DEAD`,
		shellQuote(pidPath), shellQuote(agentPath))
}
