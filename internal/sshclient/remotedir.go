package sshclient

import (
	"fmt"
	gopath "path"
	"strings"
)

// deniedTopLevel lists first path components under which a remote directory
// must never be placed. WriteFiles runs `rm -rf` on the directory, so a value
// like /nix/store or /boot/efi would be catastrophic.
var deniedTopLevel = map[string]bool{
	"nix": true, "boot": true, "dev": true, "proc": true, "sys": true,
	"run": true, "usr": true, "bin": true, "sbin": true, "lib": true,
	"lib64": true,
}

// ValidateRemoteDir checks that dir is a safe target for WriteFiles, which
// deletes and recreates it on every deploy. The path must be absolute, in
// clean form (no "..", ".", "//", or trailing slash), contain only
// [A-Za-z0-9._/-] so it can be interpolated into shell commands unquoted,
// have at least two components (so "/", "/root", "/home" are rejected), and
// not live under a system directory listed in deniedTopLevel.
func ValidateRemoteDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("remote directory must not be empty")
	}
	if !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("remote directory %q must be an absolute path", dir)
	}
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '/', r == '-':
		default:
			return fmt.Errorf("remote directory %q contains unsupported character %q (allowed: letters, digits, '.', '_', '-', '/')", dir, r)
		}
	}
	if cleaned := gopath.Clean(dir); cleaned != dir {
		return fmt.Errorf("remote directory %q must be a clean path (did you mean %q?)", dir, cleaned)
	}
	parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	if len(parts) < 2 {
		return fmt.Errorf("remote directory %q is too shallow: it is deleted and recreated on every deploy, so it must have at least two components (e.g. /root/nix)", dir)
	}
	if deniedTopLevel[parts[0]] {
		return fmt.Errorf("remote directory %q must not be under /%s", dir, parts[0])
	}
	return nil
}
