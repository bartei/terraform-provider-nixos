# terraform-provider-nixos

[![Test](https://github.com/bartei/terraform-provider-nixos/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/bartei/terraform-provider-nixos/actions/workflows/test.yml)
[![Acceptance](https://github.com/bartei/terraform-provider-nixos/actions/workflows/acceptance.yml/badge.svg?branch=main)](https://github.com/bartei/terraform-provider-nixos/actions/workflows/acceptance.yml)

A Terraform provider for managing [NixOS](https://nixos.org/) configurations on remote hosts via SSH.

## Features

- **Plan-time diffs** — Nix files are passed as a Terraform map, so `terraform plan` shows exactly which lines changed.
- **Secret key deployment** — Upload secret files with precise ownership and permissions before building.
- **Dedicated build hosts** — Offload builds to a powerful machine and transfer the closure to the target via `nix-copy-closure`.
- **Streaming output** — Build and switch output streams in real time through Terraform's logging.
- **SSH keepalive** — Long-running builds won't drop due to idle connections.

## Quick Start

```hcl
terraform {
  required_providers {
    nixos = {
      source  = "bartei/nixos"
      version = "~> 0.1"
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

resource "nixos_configuration" "this" {
  ssh_host        = "10.0.0.10"
  ssh_user        = "root"
  ssh_private_key = file("~/.ssh/id_ed25519")

  configuration_files = local.nix_files

  keys = {
    "api_key" = {
      content     = var.api_key
      destination = "/var/keys"
      user        = "root"
      group       = "keys"
      mode        = "0640"
    }
  }
}
```

Place your NixOS flake in a `nix/` directory alongside your Terraform configuration:

```
my-server/
├── main.tf
└── nix/
    ├── flake.nix
    ├── flake.lock
    └── configuration.nix
```

## Resources

| Resource | Description |
|---|---|
| [nixos_configuration](docs/resources/configuration.md) | Manages a NixOS configuration on a remote host |

## Data Sources

| Data Source | Description |
|---|---|
| [nixos_system_info](docs/data-sources/system_info.md) | Reads runtime info from a NixOS system |

## Development

```bash
# Build
make build

# Install to local plugin cache
make install

# Print dev_overrides for ~/.terraformrc
make dev
```

### Acceptance tests

End-to-end tests that run a real `terraform apply` against a NixOS VM.

**Prerequisites (local):**

- [Nix](https://nixos.org/download.html) (for building the test VM image)
- [QEMU](https://www.qemu.org/) — `qemu-system-x86_64` and `qemu-img`
- KVM access (`/dev/kvm` readable by your user) recommended; tests fall back
  to TCG emulation otherwise but it's much slower
- `terraform` on `$PATH` (the test framework downloads it if missing)

**Running locally:**

```bash
# Build the VM image and boot it (one-time per session):
make testacc-vm-up

# Run the suite (set TF_ACC=1; reads the VM host:port from test/qemu/.vm-host):
make testacc

# Tear down:
make testacc-vm-down
```

**Environment variables consumed by the suite:**

| Var | Default | Notes |
|---|---|---|
| `TF_ACC` | _(required)_ | Must be `1` or the suite skips. |
| `NIXOS_TEST_HOST` | _(required)_ | `host:port` of the test target. |
| `NIXOS_TEST_KEY_PATH` | _(required)_ | Path to the SSH private key. |
| `NIXOS_TEST_USER` | `root` | SSH user. |

**CI:** the same suite runs on every push and pull request via
`.github/workflows/acceptance.yml`, with the QEMU VM brought up inside the
runner.

### Releasing

Push a version tag to trigger the GitHub Actions release workflow:

```bash
git tag v0.2.0
git push origin v0.2.0
```

GoReleaser builds multi-platform binaries (linux/darwin, amd64/arm64), signs the
checksums with GPG, and publishes to GitHub Releases. The Terraform Registry picks
up the release automatically.

## License

MIT