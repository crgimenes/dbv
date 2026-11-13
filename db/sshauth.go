package db

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
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

// knownHostAlgorithms lists the key types known_hosts holds for addr. Without
// it the server may present a type that is not recorded (ecdsa when only
// ed25519 is known) and knownhosts reports a mismatch where ssh would not.
func knownHostAlgorithms(cb ssh.HostKeyCallback, addr string) []string {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil
	}
	probe, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(cb(addr, &net.TCPAddr{}, probe), &keyErr) {
		return nil
	}

	var algos []string
	for _, k := range keyErr.Want {
		if certAuthority(k) {
			// The key is a CA's, not the host's: the host shows a
			// certificate of any type, so the negotiation stays open.
			return nil
		}
		switch k.Key.Type() {
		case ssh.KeyAlgoRSA:
			algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		default:
			algos = append(algos, k.Key.Type())
		}
	}
	return algos
}

// certAuthority says whether k came from an @cert-authority line. KeyError
// does not say, but it gives the file and line.
func certAuthority(k knownhosts.KnownKey) bool {
	b, err := os.ReadFile(filepath.Clean(k.Filename)) // #nosec G304 -- the known_hosts dbv opened
	if err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	if k.Line < 1 || k.Line > len(lines) {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(lines[k.Line-1]), "@cert-authority")
}
