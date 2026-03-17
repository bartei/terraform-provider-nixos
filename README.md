# terraform-provider-nixos

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