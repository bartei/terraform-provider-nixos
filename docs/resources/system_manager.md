---
page_title: "nixos_system_manager Resource - terraform-provider-nixos"
subcategory: ""
description: |-
  Manages a numtide/system-manager profile on a non-NixOS Linux host via SSH.
  Installs Nix if needed, uploads the flake, deploys secret keys and runs
  `system-manager switch`.
---

# nixos_system_manager (Resource)

Applies a [system-manager](https://github.com/numtide/system-manager) flake to a
Linux host that is **not** running NixOS (Debian, Ubuntu — e.g. a Proxmox VE node).
You get NixOS-style modules (`systemd.services`, `environment.etc`,
`environment.systemPackages`, `systemd.tmpfiles`, …) applied declaratively on top
of the distro, without replacing the OS.

On each apply the provider:

1. Checks for `/nix/var/nix/profiles/default/bin/nix`; if missing and `install_nix`
   is true, runs the official multi-user installer (`--daemon --yes`)
2. Uploads the flake files to `remote_directory`
3. Deploys secret key files with the requested ownership and permissions
4. Runs `nix run <system_manager_flake> -- switch --flake <remote_directory>#<configuration_name>`
   (build → register profile → activate units and /etc files)
5. Deletes generations of the system-manager profile older than the previous one
   (current and previous are kept) and runs `nix-store --gc` (unless
   `garbage_collect = false`)
6. Records the store hash of the active generation of
   `/nix/var/nix/profiles/system-manager-profiles/system-manager` as `system_hash`

Nix is invoked with `experimental-features = nix-command flakes` and
`accept-flake-config = true` through `NIX_CONFIG`, so `/etc/nix/nix.conf` on the
target does not need to be edited (the installer step only appends the
experimental-features line on a fresh install, for interactive use).

Destroying this resource only removes it from Terraform state. Run
`system-manager deactivate` on the host to remove the managed units and files.

## Example Usage

```hcl
locals {
  nix_files = {
    for f in fileset("${path.module}/nix", "**") :
    f => file("${path.module}/nix/${f}")
    if !startswith(f, ".") && !strcontains(f, "/.")
  }
}

resource "nixos_system_manager" "k3s" {
  ssh_host        = "10.100.0.105"
  ssh_user        = "root"
  ssh_private_key = var.ssh_private_key

  configuration_files  = local.nix_files
  system_manager_flake = "github:numtide/system-manager/release-26.05"
}
```

With a flake such as:

```nix
{
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-26.05";
    system-manager = {
      url = "github:numtide/system-manager/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, system-manager, ... }: {
    systemConfigs.default = system-manager.lib.makeSystemConfig {
      modules = [ ./system.nix ];
    };
  };
}
```

## Argument Reference

### Required

- `ssh_host` (String) — IP or hostname of the target machine.
- `ssh_user` (String) — SSH user. Must be `root`: system-manager needs root to activate.
- `configuration_files` (Map of String) — Relative file paths to contents for the
  system-manager flake. Changes to any value trigger a rebuild and switch.

### Optional

- `ssh_port` (Number) — SSH port. Default: `22`.
- `ssh_private_key` (String, Sensitive) — SSH private key for authentication.
- `ssh_use_agent` (Boolean) — Authenticate through the local ssh agent. Default: `false`.
- `configuration_name` (String) — Name of the `systemConfigs.<name>` flake output.
  Default: `"default"`.
- `remote_directory` (String) — Where the flake is uploaded on the target. Deleted
  and recreated before each upload, so it is validated at plan time the same way as
  `nixos_configuration.remote_directory` (clean absolute path, at least two
  components, not under a system directory). Default: `"/root/system-manager"`.
- `keys` (Map of Object) — Secret files to deploy before switching. Same schema as
  `nixos_configuration`'s `keys`.
- `install_nix` (Boolean) — Install Nix with the official multi-user installer when
  it is missing. Default: `true`.
- `nix_installer_url` (String) — Installer script URL. Default: `"https://nixos.org/nix/install"`.
- `system_manager_flake` (String) — Flake reference of the system-manager CLI run on
  the target. Pin it to the same release branch as your flake's `system-manager`
  input. Default: `"github:numtide/system-manager"`.
- `allow_unfree` (Boolean) — Set `NIXPKGS_ALLOW_UNFREE=1`. Default: `true`.
- `allow_insecure` (Boolean) — Set `NIXPKGS_ALLOW_INSECURE=1`. Default: `true`.
- `garbage_collect` (Boolean) — After switching, delete profile generations older than
  the previous one (the current and one previous generation are kept, so a bad deploy
  can be rolled back by hand) and run `nix-store --gc`. Default: `true`.

### Read-Only

- `id` (String) — `host:configuration_name`.
- `system_hash` (String) — Nix store hash of the active system-manager profile
  generation. Changes whenever the deployed configuration changes.

## Deployment Logging

Progress and the streamed `system-manager switch` output go through Terraform's
log system at `INFO` level, exactly like `nixos_configuration`: run with
`TF_LOG=INFO` (or `TF_LOG_PROVIDER=INFO`) to see them. Streamed lines carry a
`phase` field of `nix-install`, `switch` or `gc`.

## Notes

- The target needs `curl`, `tar` and `xz` for the Nix installer (all present on the
  stock Debian and Ubuntu cloud images).
- The Nix installer creates the `nixbld*` build users, `nix-daemon.service`, and
  adds a PATH hook to `/etc/profile.d/nix.sh` and `/etc/bash.bashrc`. It refuses to
  run if a previous partial install left backup files behind.
- system-manager rewrites `wantedBy = [ "multi-user.target" ]` to its own
  `system-manager.target`, so units written like NixOS units start on boot.
- system-manager refuses to overwrite files in `/etc` it does not manage unless the
  entry sets `replaceExisting = true`.
