package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestParseHostKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	keyLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))

	// 裸公钥行 "ssh-ed25519 AAAA..."
	got, err := parseHostKey(keyLine)
	if err != nil {
		t.Fatalf("bare key: %v", err)
	}
	if !bytes.Equal(got.Marshal(), pub.Marshal()) {
		t.Fatal("bare key mismatch")
	}

	// 带主机前缀的 known_hosts 行
	got2, err := parseHostKey("[127.0.0.1]:2222 " + keyLine)
	if err != nil {
		t.Fatalf("prefixed key: %v", err)
	}
	if !bytes.Equal(got2.Marshal(), pub.Marshal()) {
		t.Fatal("prefixed key mismatch")
	}

	// 非法
	if _, err := parseHostKey("not-a-key"); err == nil {
		t.Fatal("expected error for invalid key")
	}
}
