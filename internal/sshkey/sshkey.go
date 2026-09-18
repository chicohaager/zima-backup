// Package sshkey manages the module's own ed25519 key pair, used for ssh
// and sftp targets by both runners. Users paste the public key into
// authorized_keys on the target; the private key never leaves Dir.
package sshkey

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Pair is the key pair below Dir (mode 0700, created by the store).
type Pair struct {
	Dir string
}

// PrivatePath is the private key file; it exists after the first Public().
func (p Pair) PrivatePath() string { return filepath.Join(p.Dir, "id_ed25519") }

// KnownHostsPath is the module's own known_hosts, filled on first contact
// (StrictHostKeyChecking=accept-new) so a later key change is refused.
func (p Pair) KnownHostsPath() string { return filepath.Join(p.Dir, "known_hosts") }

// Public returns the public key line, generating the pair on first use.
func (p Pair) Public() (string, error) {
	priv := p.PrivatePath()
	if _, err := os.Stat(priv); os.IsNotExist(err) {
		if err := os.MkdirAll(p.Dir, 0700); err != nil {
			return "", err
		}
		cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "zbackup", "-f", priv)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	pub, err := os.ReadFile(priv + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
}

// Ensure generates the pair if missing, for callers that need the private
// key path rather than the public line.
func (p Pair) Ensure() error {
	_, err := p.Public()
	return err
}

// SSHArgs are the options every ssh invocation uses: our key, no prompts,
// our own known_hosts.
func (p Pair) SSHArgs() []string {
	return []string{
		"-i", p.PrivatePath(),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + p.KnownHostsPath(),
	}
}
