---
page_title: "nixos_system_info Data Source - terraform-provider-nixos"
subcategory: ""
description: |-
  Reads runtime information from a NixOS system via SSH.
---

# nixos_system_info (Data Source)

Reads runtime information from a NixOS system via SSH. Use this data source
to inspect the current state of a NixOS machine — its version, kernel,
system derivation hash, and more.

This is useful for:

- Displaying system info as Terraform outputs
- Conditional logic based on the current NixOS version or architecture
- Verifying that a deployment target is reachable and running NixOS

## Example Usage

### Basic

```hcl
data "nixos_system_info" "target" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.ssh_private_key
}

output "nixos_version" {
  value = data.nixos_system_info.target.nixos_version
}

output "system_hash" {
  value = data.nixos_system_info.target.system_hash
}
```

### With a Provisioned Host

Combine with other resources to read info from a freshly provisioned machine:

```hcl
resource "nixos_configuration" "webserver" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.ssh_private_key

  configuration_files = local.nix_files
}

data "nixos_system_info" "webserver" {
  depends_on = [nixos_configuration.webserver]

  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.ssh_private_key
}

output "deployed_version" {
  value = data.nixos_system_info.webserver.nixos_version
}
```

## Argument Reference

### Required

- `ssh_host` (String) — IP or hostname of the NixOS machine.
- `ssh_user` (String) — SSH user.
- `ssh_private_key` (String, Sensitive) — SSH private key for authentication.

## Attribute Reference

- `id` (String) — Set to the `ssh_host` value.
- `nixos_version` (String) — NixOS version string (e.g. `"24.11.20241201.abc1234"`).
  Output of `nixos-version`.
- `kernel_version` (String) — Linux kernel version (e.g. `"6.6.63"`).
  Output of `uname -r`.
- `system_hash` (String) — Nix store hash of `/run/current-system`.
  Output of `nix-store --query --hash /run/current-system`.
- `hostname` (String) — System hostname.
  Output of `hostname`.
- `architecture` (String) — CPU architecture (e.g. `"x86_64"`).
  Output of `uname -m`.
- `current_system_path` (String) — Full Nix store path of the current system derivation
  (e.g. `"/nix/store/abc123-nixos-system-hostname-24.11"`).
  Output of `readlink -f /run/current-system`.
- `uptime` (String) — Human-readable uptime (e.g. `"up 3 days, 2 hours"`).
  Output of `uptime -p`.