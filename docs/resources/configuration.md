---
page_title: "nixos_configuration Resource - terraform-provider-nixos"
subcategory: ""
description: |-
  Manages a NixOS configuration on a remote host via SSH. Uploads Nix flake
  files, deploys secret keys, builds the configuration, and switches the
  system to the new generation.
---

# nixos_configuration (Resource)

Manages a NixOS configuration on a remote host via SSH. On each apply the provider:

1. Uploads the Nix flake files to the target (or build host)
2. Deploys secret key files with specified ownership and permissions
3. Runs `nixos-rebuild build` to compile the new system derivation
4. Runs `nixos-rebuild switch` to activate it
5. Cleans up old generations and optionally garbage-collects the Nix store

Because `configuration_files` is a regular Terraform map attribute, `terraform plan`
shows a line-by-line diff of every changed Nix file before you apply.

Destroying this resource only removes it from Terraform state — the running NixOS
system is left unchanged.

## Example Usage

### Basic

```hcl
locals {
  nix_files = {
    for f in fileset("${path.module}/nix", "**") :
    f => file("${path.module}/nix/${f}")
    if !startswith(f, ".") && !contains(f, "/.")
  }
}

resource "nixos_configuration" "this" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.ssh_private_key

  configuration_files = local.nix_files
}
```

### With Secret Keys

```hcl
resource "nixos_configuration" "this" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.ssh_private_key

  configuration_files = local.nix_files

  keys = {
    "admin_password" = {
      content     = var.admin_password
      destination = "/var/keys"
      user        = "root"
      group       = "keys"
      mode        = "0640"
    }
    "tls_key" = {
      content     = var.tls_private_key
      destination = "/var/keys/ssl"
      user        = "nginx"
      group       = "nginx"
      mode        = "0600"
    }
  }
}
```

### With a Dedicated Build Host

Offload builds to a powerful machine and copy the closure to the target.
This is useful when the target is a lightweight container or VM that shouldn't
be loaded with compilation work.

```hcl
resource "nixos_configuration" "this" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.target_key

  configuration_files = local.nix_files

  build_host        = "10.0.0.99"
  build_user        = "root"
  build_private_key = var.builder_key

  garbage_collect = false  # skip GC on production
}
```

When `build_host` is set, the provider:

1. Uploads configuration files to the **build host**
2. Deploys secret keys to the **target**
3. Runs `nixos-rebuild build` on the **build host**
4. Temporarily deploys the target SSH key to the build host
5. Runs `nix-copy-closure` from the build host to the target
6. Activates the new configuration on the target via `switch-to-configuration switch`

### Inline Configuration Files

Instead of reading files from disk, you can pass Nix configuration inline:

```hcl
resource "nixos_configuration" "this" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.ssh_private_key

  configuration_files = {
    "flake.nix" = <<-NIX
      {
        inputs.nixpkgs.url = "github:nixos/nixpkgs/nixos-24.11";
        outputs = { nixpkgs, ... }: {
          nixosConfigurations.this = nixpkgs.lib.nixosSystem {
            system = "x86_64-linux";
            modules = [ ./configuration.nix ];
          };
        };
      }
    NIX

    "configuration.nix" = <<-NIX
      { pkgs, ... }: {
        services.openssh.enable = true;
        system.stateVersion = "24.11";
      }
    NIX
  }
}
```

### Using ssh agent

Deligate authentication to ssh-agent

```
resource "nixos_configuration" "this" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_use_agent   = true

  configuration_files = local.nix_files
}
```

## Argument Reference

### Required

- `ssh_host` (String) — IP or hostname of the target NixOS machine.
- `ssh_user` (String) — SSH user for the target machine.
- `configuration_files` (Map of String) — Map of relative file paths to their contents
  for the NixOS flake. Changes to any value trigger a rebuild and switch.

### Optional

- `ssh_private_key` (String, Sensitive) — SSH private key for authentication. (does nothing if `ssh_use_agent` is true)
- `ssh_use_agent` (Bool) — Use ssh-agent to connect to target.
- `configuration_name` (String) — Name of the NixOS configuration output in the flake.
  Default: `"this"`.
- `remote_directory` (String) — Remote directory where the configuration is uploaded.
  This directory is **cleaned before each upload** to ensure a consistent state.
  Default: `"/root/nix"`.
- `keys` (Map of Object) — Secret files to deploy to the target before building.
  Each key in the map becomes the filename. See [Nested Schema for `keys`](#nested-schema-for-keys).
- `build_host` (String) — SSH host of a dedicated build machine. When set, the
  NixOS configuration is built here and the closure is copied to the target.
- `build_user` (String) — SSH user for the build host. Default: `"root"`.
- `build_private_key` (String, Sensitive) — SSH private key for the build host. (does nothing if `build_use_agent` is true)
- `build_use_agent` (Bool) — Use ssh-agent to connect to build host.
- `allow_unfree` (Boolean) — Set `NIXPKGS_ALLOW_UNFREE=1` during build. Default: `true`.
- `allow_insecure` (Boolean) — Set `NIXPKGS_ALLOW_INSECURE=1` during build. Default: `true`.
- `garbage_collect` (Boolean) — Run `nix-store --gc` after switching. Default: `true`.

### Read-Only

- `id` (String) — Resource identifier in the format `host:configuration_name`.
- `system_hash` (String) — Nix store hash of `/run/current-system` after deployment.

### Nested Schema for `keys`

Each entry in the `keys` map accepts:

- `content` (String, Sensitive, Required) — File content.
- `destination` (String, Optional) — Directory on the target to place the file.
  Default: `"/var/keys"`.
- `user` (String, Required) — Owner user.
- `group` (String, Required) — Owner group.
- `mode` (String, Required) — File permission mode passed to `chmod` (e.g. `"0640"`).

## Deployment Logging

The provider streams build output through Terraform's log system. To see it, set
the log level:

```bash
TF_LOG=INFO terraform apply
```

Log prefixes indicate the phase:

| Prefix | Phase |
|---|---|
| `[build]` | `nixos-rebuild build` output |
| `[switch]` | `nixos-rebuild switch` output |
| `[copy-closure]` | `nix-copy-closure` transfer (build host mode) |
| `[gc]` | Nix garbage collection |
| `[git-install]` | Git installation on the build host |
