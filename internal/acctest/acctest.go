// Package acctest contains shared helpers for acceptance tests of the nixos
// provider. Acceptance tests run a real `terraform apply` against an SSH-
// reachable target (in CI, a NixOS-in-docker container brought up by
// test/docker/run.sh).
package acctest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/bartei/terraform-provider-nixos/internal/provider"
)

// ProviderName is the type prefix used in HCL fixtures (`nixos_configuration`).
const ProviderName = "nixos"

// ProviderFactories returns an in-process provider factory map for use with
// resource.TestCase{ProtoV6ProviderFactories: ...}.
func ProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		ProviderName: providerserver.NewProtocol6WithError(provider.New("acctest")()),
	}
}

// Target describes how an acceptance test reaches the NixOS box. All fields
// are populated from environment variables set by the test harness or CI:
//
//	NIXOS_TEST_HOST       — host:port string (e.g. "127.0.0.1:32789")
//	NIXOS_TEST_USER       — ssh user (default "root")
//	NIXOS_TEST_KEY_PATH   — path to the private key file
type Target struct {
	Host       string
	Port       string
	User       string
	KeyPath    string
	PrivateKey string // PEM contents
}

// PreCheck must be called from every acceptance test's PreCheck closure. It
// fails the test if TF_ACC is unset or the target is unreachable.
func PreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance test")
	}
	tg, err := TargetFromEnv()
	if err != nil {
		t.Fatalf("acctest target not configured: %v", err)
	}
	if err := waitForSSH(tg, 10*time.Second); err != nil {
		t.Fatalf("target %s:%s unreachable: %v", tg.Host, tg.Port, err)
	}
}

// TargetFromEnv reads NIXOS_TEST_* env vars and returns a populated Target.
func TargetFromEnv() (Target, error) {
	hp := os.Getenv("NIXOS_TEST_HOST")
	if hp == "" {
		return Target{}, fmt.Errorf("NIXOS_TEST_HOST is not set (expected host:port)")
	}
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return Target{}, fmt.Errorf("NIXOS_TEST_HOST %q: %w", hp, err)
	}

	keyPath := os.Getenv("NIXOS_TEST_KEY_PATH")
	if keyPath == "" {
		return Target{}, fmt.Errorf("NIXOS_TEST_KEY_PATH is not set")
	}
	keyPath, err = filepath.Abs(keyPath)
	if err != nil {
		return Target{}, fmt.Errorf("resolving NIXOS_TEST_KEY_PATH: %w", err)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return Target{}, fmt.Errorf("reading key %s: %w", keyPath, err)
	}

	user := os.Getenv("NIXOS_TEST_USER")
	if user == "" {
		user = "root"
	}

	return Target{
		Host:       host,
		Port:       port,
		User:       user,
		KeyPath:    keyPath,
		PrivateKey: string(keyBytes),
	}, nil
}

// waitForSSH dials the target's SSH port until it accepts connections or the
// deadline expires. It does not authenticate — just confirms the listener is
// up.
func waitForSSH(t Target, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(t.Host, t.Port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

// StartAgent launches a transient ssh-agent, adds the test private key to it,
// and returns the agent socket path. The agent is killed via t.Cleanup.
//
// We start a child `ssh-agent -D` (foreground daemon) bound to a unix socket
// in t.TempDir so multiple parallel test runs don't collide.
func StartAgent(t *testing.T, target Target) string {
	t.Helper()

	socketPath := filepath.Join(t.TempDir(), "agent.sock")

	raw, err := ssh.ParseRawPrivateKey([]byte(target.PrivateKey))
	if err != nil {
		t.Fatalf("parsing test private key: %v", err)
	}

	cmd := exec.Command("ssh-agent", "-a", socketPath, "-D")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting ssh-agent: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.Remove(socketPath)
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dialing ssh-agent socket: %v", err)
	}
	defer conn.Close()

	if err := agent.NewClient(conn).Add(agent.AddedKey{PrivateKey: raw}); err != nil {
		t.Fatalf("adding key to ssh-agent: %v", err)
	}

	return socketPath
}

// SSHClient opens an authenticated SSH connection to the target, useful for
// post-apply assertions.
func SSHClient(t *testing.T, target Target) *ssh.Client {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(target.PrivateKey))
	if err != nil {
		t.Fatalf("parsing key: %v", err)
	}
	cfg := &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	cli, err := ssh.Dial("tcp", net.JoinHostPort(target.Host, target.Port), cfg)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// RunRemote runs cmd over an existing client and returns trimmed stdout.
func RunRemote(t *testing.T, cli *ssh.Client, cmd string) string {
	t.Helper()
	sess, err := cli.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		t.Fatalf("remote cmd %q failed: %v\n%s", cmd, err, out)
	}
	return string(out)
}
