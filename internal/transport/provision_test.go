package transport

import (
	"errors"
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/tmp/skipper-agent": `'/tmp/skipper-agent'`,
		"a b":                `'a b'`,
		"it's":               `'it'\''s'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLaunchCommand(t *testing.T) {
	spec := ProvisionSpec{
		RemotePath: "/tmp/skipper-agent",
		ServerAddr: "127.0.0.1:7600",
		NodeName:   "gpu-docker-07",
		Partition:  "gpu",
		LogPath:    "/tmp/skipper-agent.log",
		PidPath:    "/tmp/skipper-agent.pid",
	}
	cmd := launchCommand(spec)
	for _, want := range []string{
		"nohup '/tmp/skipper-agent'",
		"--server '127.0.0.1:7600'",
		"--name 'gpu-docker-07'",
		"--partition 'gpu'",
		"</dev/null >'/tmp/skipper-agent.log' 2>&1",
		"& echo $! > '/tmp/skipper-agent.pid'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("launchCommand missing %q in %q", want, cmd)
		}
	}

	// 空分区应省略 --partition；额外参数应透传。
	spec.Partition = ""
	spec.ExtraArgs = "--labels rack=a1"
	cmd = launchCommand(spec)
	if strings.Contains(cmd, "--partition") {
		t.Errorf("expected no --partition when empty: %q", cmd)
	}
	if !strings.Contains(cmd, "--labels rack=a1") {
		t.Errorf("expected extra args passed through: %q", cmd)
	}
}

func TestScpControl(t *testing.T) {
	if got := scpControl(100, "skipper-agent"); got != "C0755 100 skipper-agent\n" {
		t.Errorf("scpControl = %q", got)
	}
}

func TestAliveCommand(t *testing.T) {
	cmd := aliveCommand("/tmp/skipper-agent.pid", "/tmp/skipper-agent")
	for _, want := range []string{
		"cat '/tmp/skipper-agent.pid'",
		"kill -0",
		"/proc/[0-9]*/cmdline",
		"grep -qF '/tmp/skipper-agent'",
		"echo ALIVE",
		"echo DEAD",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("aliveCommand missing %q in %q", want, cmd)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		dialErr error
		procErr error
		alive   bool
		want    DiagCause
	}{
		{"ssh down", errors.New("dial fail"), nil, false, DiagSSHDown},
		{"ssh down beats all", errors.New("dial fail"), errors.New("x"), true, DiagSSHDown},
		{"proc check error", nil, errors.New("session fail"), false, DiagOther},
		{"agent killed", nil, nil, false, DiagAgentKilled},
		{"all healthy but no heartbeat", nil, nil, true, DiagOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.dialErr, c.procErr, c.alive)
			if got.Cause != c.want {
				t.Errorf("classify = %q, want %q (detail=%q)", got.Cause, c.want, got.Detail)
			}
			if got.Detail == "" {
				t.Error("expected non-empty detail")
			}
		})
	}
}
