---
page_title: "NixOS Provider"
subcategory: ""
description: |-
  The NixOS provider manages NixOS configurations on remote hosts via SSH.
---

# NixOS Provider

The NixOS provider enables declarative management of [NixOS](https://nixos.org/) systems
through Terraform. It connects to remote hosts over SSH, uploads Nix flake configurations,
deploys secret files, and performs `nixos-rebuild` operations — all with full plan-time
diffing and streaming build output.

## Key Features

- **Configuration-as-code**: Nix flake files are passed as a Terraform map, giving you
  native plan-time diffs showing exactly which lines of your NixOS configuration changed.
- **Secret management**: Deploy secret files (API keys, passwords, certificates) to the
  target host with precise ownership and permission control.
- **Dedicated build hosts**: Offload `nixos-rebuild` to a powerful build machine and
  transfer the closure to the target, keeping production systems under low load and
  leveraging shared Nix caches.
- **Streaming output**: Build and switch output is streamed through Terraform's logging
  so you see progress in real time.
- **SSH keepalive**: Long-running builds (10+ minutes) won't drop due to idle timeouts.

## Example Usage

```hcl
terraform {
  required_providers {
    nixos = {
      source = "bartei/nixos"
    }
  }
}

provider "nixos" {}

locals {
  nix_files = {
    for f in fileset("${path.module}/nix", "**") :
    f => file("${path.module}/nix/${f}")
    if !startswith(f, ".") && !contains(f, "/.")
  }
}

resource "nixos_configuration" "webserver" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = file("~/.ssh/id_ed25519")

  configuration_files = local.nix_files
  configuration_name  = "this"

  keys = {
    "tls_cert" = {
      content     = file("certs/server.pem")
      destination = "/var/keys"
      user        = "nginx"
      group       = "nginx"
      mode        = "0640"
    }
  }
}
```

## Example with a Dedicated Build Host

```hcl
resource "nixos_configuration" "production" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = var.target_ssh_key

  configuration_files = local.nix_files
  configuration_name  = "this"

  # Build on a dedicated machine instead of the target
  build_host        = "10.0.0.99"
  build_user        = "root"
  build_private_key = var.builder_ssh_key

  # The build host must be able to SSH to the target.
  # The provider handles this by temporarily deploying
  # the target's SSH key to the build host for the
  # nix-copy-closure transfer.
}
```

## Authentication

The provider authenticates to remote hosts using SSH private keys. Keys are passed
per-resource (not at the provider level), allowing you to manage multiple hosts with
different credentials in the same Terraform configuration.

All SSH connections use:
- Public key authentication only
- No host key verification (suitable for internal/infrastructure networks)
- 30-second connection timeout
- 30-second keepalive interval

## Schema

The provider block accepts no configuration. All settings are per-resource.

```hcl
provider "nixos" {}
```