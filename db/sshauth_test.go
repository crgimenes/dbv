package db

import (
	"crypto/ed25519"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestAgentAuthLockedKeyWithoutAgent(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	err = os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = agentAuth(keyPath)
	if err == nil || !strings.Contains(err.Error(), "run ssh-add "+keyPath) {
		t.Fatalf("agentAuth(locked key) = %v, want a hint to run ssh-add", err)
	}
}
