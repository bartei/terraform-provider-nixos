package sshclient

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

// vmClient connects to the acceptance VM described by NIXOS_TEST_* (see
// internal/acctest), or skips the test when it is not configured.
func vmClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance test")
	}
	hp := os.Getenv("NIXOS_TEST_HOST")
	keyPath := os.Getenv("NIXOS_TEST_KEY_PATH")
	if hp == "" || keyPath == "" {
		t.Skip("NIXOS_TEST_HOST / NIXOS_TEST_KEY_PATH not set")
	}
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		t.Fatalf("NIXOS_TEST_HOST %q: %v", hp, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading key: %v", err)
	}
	user := os.Getenv("NIXOS_TEST_USER")
	if user == "" {
		user = "root"
	}
	c, err := New(host, port, user, false, string(key))
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestAcc_WriteFile_AppliesModeViaHandle verifies that WriteFile's chmod on
// the open SFTP handle (fsetstat) takes effect on the remote host, both for a
// new file and when overwriting an existing one, and that content round-trips.
func TestAcc_WriteFile_AppliesModeViaHandle(t *testing.T) {
	c := vmClient(t)
	const path = "/root/.tf-nixos-writefile-test"
	t.Cleanup(func() { _, _, _ = c.Run("rm -f " + path) })

	for i, tc := range []struct {
		content string
		mode    os.FileMode
		want    string
	}{
		{"first", 0600, "600"},
		{"second-overwrite", 0640, "640"},
		{"third", 0600, "600"},
	} {
		if err := c.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
			t.Fatalf("step %d: WriteFile: %v", i, err)
		}
		mode, _, err := c.Run("stat -c %a " + path)
		if err != nil {
			t.Fatalf("step %d: stat: %v", i, err)
		}
		if got := strings.TrimSpace(mode); got != tc.want {
			t.Errorf("step %d: mode = %q, want %q", i, got, tc.want)
		}
		content, _, err := c.Run("cat " + path)
		if err != nil {
			t.Fatalf("step %d: cat: %v", i, err)
		}
		if content != tc.content {
			t.Errorf("step %d: content = %q, want %q", i, content, tc.content)
		}
	}
}

// TestAcc_WriteFiles_RefusesUnsafeDir verifies the runtime guard in WriteFiles
// rejects a dangerous base directory before running anything remotely.
func TestAcc_WriteFiles_RefusesUnsafeDir(t *testing.T) {
	c := vmClient(t)
	marker := "/root/.tf-nixos-guard-marker"
	if _, _, err := c.Run("touch " + marker); err != nil {
		t.Fatalf("touch marker: %v", err)
	}
	t.Cleanup(func() { _, _, _ = c.Run("rm -f " + marker) })

	err := c.WriteFiles("/root", map[string]string{"x": "y"})
	if err == nil || !strings.Contains(err.Error(), "refusing to clean remote directory") {
		t.Fatalf("WriteFiles(/root) error = %v, want refusal", err)
	}
	if _, _, err := c.Run("test -f " + marker); err != nil {
		t.Fatalf("marker file was removed: the guard did not prevent rm -rf")
	}
}
