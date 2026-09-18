// Package sshkey manages the module's own ed25519 key pair, used for ssh
// and sftp targets by both runners. Users paste the public key into
// authorized_keys on the target; the private key never leaves Dir.
package sshkey

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrHostUnreachable says a host did not answer the host key scan.
var ErrHostUnreachable = errors.New("host unreachable")

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

// HostKeyTypes returns the key types known_hosts holds for host (OpenSSH
// records the one type it negotiated, e.g. only ssh-ed25519). rclone's
// ssh client may negotiate a different type and then fails with
// "knownhosts: key mismatch" (measured with rclone 1.74 against a host
// recorded by OpenSSH) — callers pass the returned types as its host key
// algorithms. A host not yet recorded is scanned once and appended, the
// same trust-on-first-use OpenSSH applies with accept-new.
func (p Pair) HostKeyTypes(host string, port int) ([]string, error) {
	if types := p.recordedTypes(host, port); len(types) > 0 {
		return types, nil
	}
	args := []string{"-T", "5"}
	if port != 0 && port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	out, err := exec.Command("ssh-keyscan", append(args, host)...).Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("%w: host key of %s could not be read", ErrHostUnreachable, host)
	}
	if err := os.MkdirAll(p.Dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.KnownHostsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(out)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, werr
	}
	return p.recordedTypes(host, port), nil
}

// recordedTypes lists the key types of plain (unhashed) known_hosts
// entries for host, written as "host" or "[host]:port".
func (p Pair) recordedTypes(host string, port int) []string {
	data, err := os.ReadFile(p.KnownHostsPath())
	if err != nil {
		return nil
	}
	want := map[string]bool{host: true}
	if port != 0 && port != 22 {
		want = map[string]bool{"[" + host + "]:" + strconv.Itoa(port): true}
	}
	var types []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		for _, h := range strings.Split(fields[0], ",") {
			if want[h] {
				types = append(types, fields[1])
				break
			}
		}
	}
	return types
}
