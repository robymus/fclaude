package install

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// EnsureKey creates an ed25519 keypair at path (OpenSSH private key, and
// path+".pub" as an authorized_keys line) unless the private key already
// exists. It returns the public line. An existing key is kept: the VM's
// authorized_keys already trusts it (restore case).
func EnsureKey(path, comment string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		s, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return "", fmt.Errorf("existing key %s is unreadable: %w", path, err)
		}
		return authorizedLine(s.PublicKey(), comment), nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	blk, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", err
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return "", err
	}
	line := authorizedLine(pub, comment)
	if err := writeAtomic(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := writeAtomic(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		return "", err
	}
	// Windows OpenSSH (ssh.exe, used for interactive CLI sessions) refuses a
	// private key other users can read.
	if err := restrictToOwner(path); err != nil {
		return "", fmt.Errorf("restrict %s: %w", path, err)
	}
	return line, nil
}

func authorizedLine(k ssh.PublicKey, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " " + comment
}
