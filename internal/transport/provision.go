package transport

// 本文件实现 Agent 自举：控制平面经一条临时 SSH 连接，把对应架构的 agent 可执行文件
// 通过 SCP 协议(等价 `scp`)推送到节点，再远程后台拉起 agent。适配「仅开放 SSH 端口」的
// 容器：除 SSH 外无需任何额外通道，也无需在节点预装 agent。

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// scpTimeout 限制单次二进制上传时长，避免远端无响应时永久阻塞。
const scpTimeout = 60 * time.Second

// ProvisionSpec 描述把 agent 二进制推送到节点并远程拉起所需的参数。
type ProvisionSpec struct {
	LocalBin   string // 控制平面侧 agent 可执行文件路径（按目标架构选择 amd64/arm64）
	RemotePath string // 推送到节点内的目标路径，如 /tmp/skipper-agent
	ServerAddr string // agent --server 指向的地址（即隧道在容器回环的监听 RemoteListen）
	NodeName   string // agent --name；须与隧道/诊断时的节点名一致，便于失联时对应到配置
	Partition  string // agent --partition（可空）
	ExtraArgs  string // 透传给 agent 的额外参数（可空，使用方自负引号）
	LogPath    string // agent 在节点内的日志文件
	PidPath    string // agent pid 文件，供失联诊断判断进程是否存活
}

// Provision 通过一条临时 SSH 连接把 agent 二进制(SCP)推送到节点并远程后台拉起。
func Provision(ctx context.Context, cfg Config, spec ProvisionSpec, log *slog.Logger) error {
	if spec.LocalBin == "" {
		return fmt.Errorf("provision: agent_bin 未配置")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := dialSSH(cfg, log)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := uploadSCP(client, spec.LocalBin, spec.RemotePath); err != nil {
		return fmt.Errorf("scp agent binary: %w", err)
	}
	if err := runCommand(client, launchCommand(spec)); err != nil {
		return fmt.Errorf("launch agent: %w", err)
	}
	return nil
}

// launchCommand 构造远程后台拉起 agent 的命令（纯函数，便于测试）：
// nohup 屏蔽 SIGHUP、stdio 重定向到节点本地日志、把 pid 写入文件供诊断使用。
func launchCommand(spec ProvisionSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "nohup %s --server %s --name %s",
		shellQuote(spec.RemotePath), shellQuote(spec.ServerAddr), shellQuote(spec.NodeName))
	if spec.Partition != "" {
		fmt.Fprintf(&b, " --partition %s", shellQuote(spec.Partition))
	}
	if strings.TrimSpace(spec.ExtraArgs) != "" {
		b.WriteString(" " + strings.TrimSpace(spec.ExtraArgs))
	}
	fmt.Fprintf(&b, " </dev/null >%s 2>&1 & echo $! > %s",
		shellQuote(spec.LogPath), shellQuote(spec.PidPath))
	return b.String()
}

// runCommand 在远端执行一条短命令并等待结束（用于拉起 agent、诊断等）。
func runCommand(client *ssh.Client, cmd string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	if out, err := sess.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// uploadSCP 用 SCP 协议(sink 模式，等价 `scp -t`)把本地文件写到远端 remotePath。
// 仅依赖远端存在 scp，无需开放额外端口或启用 sftp 子系统。
func uploadSCP(client *ssh.Client, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	w, err := sess.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	r, err := sess.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := sess.Start("scp -t " + shellQuote(remotePath)); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// 在独立 goroutine 中跑 SCP 协议握手，主协程用超时兜底，避免远端无响应永久阻塞。
	errc := make(chan error, 1)
	go func() { errc <- writeSCP(w, r, f, info.Size(), filepath.Base(remotePath)) }()

	select {
	case err := <-errc:
		if err != nil {
			_ = sess.Close()
			return err
		}
	case <-time.After(scpTimeout):
		_ = sess.Close()
		return fmt.Errorf("scp upload timed out after %s", scpTimeout)
	}
	// 协议已收到末尾 ack(传输成功)，远端退出码仅作收尾，不因其(及通道收尾竞态)误判失败；
	// 真正的失败(如远端无 scp)会在前面的 ack 阶段以 EOF/错误字节暴露。
	_ = sess.Wait()
	return nil
}

// writeSCP 按 SCP sink 协议写出文件：控制行 → 文件内容 → 结束零字节，每步校验远端应答。
func writeSCP(w io.WriteCloser, r io.Reader, content io.Reader, size int64, name string) error {
	if err := readAck(r); err != nil {
		return fmt.Errorf("await ready: %w", err)
	}
	if _, err := io.WriteString(w, scpControl(size, name)); err != nil {
		return fmt.Errorf("send control: %w", err)
	}
	if err := readAck(r); err != nil {
		return fmt.Errorf("await control ack: %w", err)
	}
	if _, err := io.Copy(w, content); err != nil {
		return fmt.Errorf("send data: %w", err)
	}
	// 单个 0 字节表示文件内容结束。
	if _, err := w.Write([]byte{0}); err != nil {
		return fmt.Errorf("send end: %w", err)
	}
	if err := readAck(r); err != nil {
		return fmt.Errorf("await data ack: %w", err)
	}
	// 收到最后一个 ack 即代表文件已写入成功；关闭 stdin 仅为收尾，
	// 此时远端可能已发 exit-status 并关闭通道，其错误(如 EOF)无关紧要。
	_ = w.Close()
	return nil
}

// scpControl 生成 SCP 控制行 "C<mode> <size> <name>\n"（纯函数，便于测试）；
// agent 需可执行，固定 0755。
func scpControl(size int64, name string) string {
	return fmt.Sprintf("C%04o %d %s\n", 0o755, size, name)
}

// readAck 读取并解释 SCP 的一个应答字节：0=成功，1/2=远端告警/致命错误。
func readAck(r io.Reader) error {
	b := make([]byte, 1)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	switch b[0] {
	case 0:
		return nil
	case 1, 2:
		msg, _ := readLine(r)
		return fmt.Errorf("scp remote: %s", msg)
	default:
		return fmt.Errorf("scp protocol: unexpected response 0x%02x", b[0])
	}
}

// readLine 读到换行为止（用于读取 SCP 错误信息）。
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
	return b.String(), nil
}

// shellQuote 用单引号安全包裹一个 shell 参数。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
