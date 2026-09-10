package sshclient

import "testing"

func TestValidateRemoteDir(t *testing.T) {
	valid := []string{
		"/root/nix",
		"/etc/nixos",
		"/var/lib/terraform-nixos",
		"/home/deploy/nix-config",
		"/tmp/tf.nixos_v2-build",
		"/opt/a/b/c/d",
	}
	for _, p := range valid {
		if err := ValidateRemoteDir(p); err != nil {
			t.Errorf("ValidateRemoteDir(%q) = %v, want nil", p, err)
		}
	}

	invalid := map[string]string{
		"":                   "empty",
		"/":                  "root",
		"/root":              "too shallow",
		"/home":              "too shallow",
		"/tmp":               "too shallow",
		"/etc":               "too shallow",
		"root/nix":           "relative",
		"~/nix":              "relative with tilde",
		"./nix":              "relative dot",
		"/root/nix/":         "trailing slash",
		"/root//nix":         "double slash",
		"/root/../nix":       "dotdot",
		"/root/nix/..":       "trailing dotdot",
		"/root/./nix":        "dot segment",
		"/root/nix;rm -rf /": "shell metacharacters",
		"/root/my nix":       "space",
		"/root/nix$HOME":     "dollar",
		"/root/nix`id`":      "backtick",
		"/root/nix'":         "quote",
		"/nix/store":         "under /nix",
		"/nix/var/nix":       "under /nix",
		"/boot/efi":          "under /boot",
		"/usr/share/x":       "under /usr",
		"/run/keys":          "under /run",
		"/dev/shm/x":         "under /dev",
		"/proc/1/x":          "under /proc",
		"/sys/kernel/x":      "under /sys",
		"/bin/x":             "under /bin",
		"/lib/x":             "under /lib",
	}
	for p, why := range invalid {
		if err := ValidateRemoteDir(p); err == nil {
			t.Errorf("ValidateRemoteDir(%q) = nil, want error (%s)", p, why)
		}
	}
}
