package db

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// agentAuth prefers ssh-agent, so a key with a passphrase works; the
// returned func closes the agent socket once the handshake is over.
// Same as devmux/ssh.go agentAuth.
func agentAuth(sshKeyPath string) (ssh.AuthMethod, func(), error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock != "" {
		// #nosec G704 -- sock is the ssh-agent socket from the user's own
		// environment, not a network address taken from any request.
		conn, err := net.DialTimeout("unix", sock, sshHandshakeWait)
		if err == nil {
			// an agent that never answers (a locked keychain prompt, a
			// wedged forwarder) fails the attempt instead of hanging it
			_ = conn.SetDeadline(time.Now().Add(sshHandshakeWait))
			return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), func() { _ = conn.Close() }, nil
		}
	}

	if sshKeyPath == "" {
		return nil, nil, errors.New("no ssh-agent and no SSH key (sshkey or the alias's IdentityFile)")
	}
	key, err := os.ReadFile(filepath.Clean(sshKeyPath))
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if _, ok := errors.AsType[*ssh.PassphraseMissingError](err); ok {
		return nil, nil, fmt.Errorf("%s is passphrase protected and no ssh-agent holds it: run ssh-add %s", sshKeyPath, sshKeyPath)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", sshKeyPath, err)
	}
	return ssh.PublicKeys(signer), func() {}, nil
}
