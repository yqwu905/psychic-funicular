package transport

// 用进程内的 SSH 服务端(x/crypto/ssh)端到端验证 agent 分发与失联诊断：
// 真实跑通 dialSSH → uploadSCP(SCP sink 协议) → runCommand(拉起) → Diagnose(进程探测)，
// 覆盖纯函数测试无法触及的 SSH I/O 与协议时序。

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeSSHServer 是仅用于测试的最小 SSH 服务端：接受任意公钥认证，
// 对 exec 请求模拟 scp sink、进程存活探测与 agent 拉起。
type fakeSSHServer struct {
	ln       net.Listener
	hostKey  ssh.Signer
	hostLine string // 主机公钥 authorized_keys 行(用于客户端 known_host 校验)

	mu        sync.Mutex
	execs     []string          // 记录收到的 exec 命令
	uploads   map[string][]byte // remotePath -> 上传内容
	aliveResp string            // 存活探测命令的应答："ALIVE" 或 "DEAD"
}

func newFakeSSHServer(t *testing.T) *fakeSSHServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSSHServer{
		ln:        ln,
		hostKey:   hostSigner,
		hostLine:  strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))),
		uploads:   map[string][]byte{},
		aliveResp: "ALIVE",
	}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSSHServer) addr() string { return s.ln.Addr().String() }

func (s *fakeSSHServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c)
	}
}

func (s *fakeSSHServer) handleConn(c net.Conn) {
	conf := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil },
	}
	conf.AddHostKey(s.hostKey)
	sconn, chans, reqs, err := ssh.NewServerConn(c, conf)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go s.handleChannel(ch, creqs)
	}
}

func (s *fakeSSHServer) handleChannel(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var p struct{ Command string }
		_ = ssh.Unmarshal(req.Payload, &p)
		_ = req.Reply(true, nil)
		s.runExec(ch, p.Command)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
		return
	}
}

func (s *fakeSSHServer) runExec(ch ssh.Channel, cmd string) {
	s.mu.Lock()
	s.execs = append(s.execs, cmd)
	resp := s.aliveResp
	s.mu.Unlock()

	switch {
	case strings.HasPrefix(cmd, "scp -t "):
		path := strings.Trim(strings.TrimPrefix(cmd, "scp -t "), "'")
		s.scpSink(ch, path)
	case strings.Contains(cmd, "echo ALIVE"): // 存活探测命令
		_, _ = io.WriteString(ch, resp+"\n")
	default: // agent 拉起或其他命令：成功且无输出
	}
}

// scpSink 模拟 `scp -t` 接收端：应答 → 读控制行 → 应答 → 读内容 → 读结束字节 → 应答。
func (s *fakeSSHServer) scpSink(ch ssh.Channel, path string) {
	if _, err := ch.Write([]byte{0}); err != nil { // 初始 ack
		return
	}
	line, err := readLine(ch) // "C0755 <size> <name>"
	if err != nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) != 3 || !strings.HasPrefix(fields[0], "C") {
		return
	}
	size, err := strconv.Atoi(fields[1])
	if err != nil {
		return
	}
	if _, err := ch.Write([]byte{0}); err != nil { // ack 控制行
		return
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(ch, buf); err != nil {
		return
	}
	z := make([]byte, 1) // 结束零字节
	if _, err := io.ReadFull(ch, z); err != nil {
		return
	}
	if _, err := ch.Write([]byte{0}); err != nil { // ack 内容
		return
	}
	s.mu.Lock()
	s.uploads[path] = buf
	s.mu.Unlock()
}

func (s *fakeSSHServer) setAlive(resp string) {
	s.mu.Lock()
	s.aliveResp = resp
	s.mu.Unlock()
}

func (s *fakeSSHServer) sawExec(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.execs {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// testConfig 返回指向 fake server 的 Config，并落地一份客户端私钥。
func testConfig(t *testing.T, s *fakeSSHServer) Config {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		Name: "test-node", Addr: s.addr(), User: "tester",
		KeyPath: keyPath, KnownHost: s.hostLine, RemoteListen: defaultTestListen,
	}
}

const defaultTestListen = "127.0.0.1:7600"

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestUploadSCPRoundTrip(t *testing.T) {
	s := newFakeSSHServer(t)
	cfg := testConfig(t, s)

	content := []byte("\x7fELF fake skipper-agent binary\x00\x01\x02payload")
	binPath := filepath.Join(t.TempDir(), "skipper-agent")
	if err := os.WriteFile(binPath, content, 0o755); err != nil {
		t.Fatal(err)
	}

	client, err := dialSSH(cfg, quietLogger())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := uploadSCP(client, binPath, "/tmp/skipper-agent"); err != nil {
		t.Fatalf("uploadSCP: %v", err)
	}

	s.mu.Lock()
	got := s.uploads["/tmp/skipper-agent"]
	s.mu.Unlock()
	if string(got) != string(content) {
		t.Fatalf("uploaded content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}
}

func TestProvisionAndDiagnose(t *testing.T) {
	s := newFakeSSHServer(t)
	cfg := testConfig(t, s)

	binPath := filepath.Join(t.TempDir(), "skipper-agent")
	if err := os.WriteFile(binPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := ProvisionSpec{
		LocalBin: binPath, RemotePath: "/tmp/skipper-agent",
		ServerAddr: defaultTestListen, NodeName: "test-node", Partition: "gpu",
		LogPath: "/tmp/skipper-agent.log", PidPath: "/tmp/skipper-agent.pid",
	}

	if err := Provision(t.Context(), cfg, spec, quietLogger()); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if _, ok := s.uploads["/tmp/skipper-agent"]; !ok {
		t.Fatal("expected binary uploaded during provision")
	}
	if !s.sawExec("nohup '/tmp/skipper-agent'") {
		t.Fatalf("expected launch command, execs=%v", s.execs)
	}

	// 进程存活 → 其他原因(SSH 与进程都正常但无心跳)。
	s.setAlive("ALIVE")
	if d := Diagnose(t.Context(), cfg, spec.PidPath, spec.RemotePath, quietLogger()); d.Cause != DiagOther {
		t.Fatalf("alive: got %q, want %q (%s)", d.Cause, DiagOther, d.Detail)
	}

	// 进程不在 → agent 进程被杀。
	s.setAlive("DEAD")
	if d := Diagnose(t.Context(), cfg, spec.PidPath, spec.RemotePath, quietLogger()); d.Cause != DiagAgentKilled {
		t.Fatalf("dead: got %q, want %q (%s)", d.Cause, DiagAgentKilled, d.Detail)
	}
}

func TestDiagnoseSSHDown(t *testing.T) {
	// 取一个随即关闭的端口，确保连接被拒。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cfg := Config{Name: "gone", Addr: addr, User: "x", KeyPath: writeThrowawayKey(t)}
	d := Diagnose(t.Context(), cfg, "/tmp/x.pid", "/tmp/x", quietLogger())
	if d.Cause != DiagSSHDown {
		t.Fatalf("got %q, want %q (%s)", d.Cause, DiagSSHDown, d.Detail)
	}
}

func writeThrowawayKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(p, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
