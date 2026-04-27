#!/usr/bin/env bash
# Build the NixOS QCOW2 image for acceptance tests.
#
# Generates an ed25519 keypair on first run (persisted in test/qemu/.keys/)
# and bakes the public key into the image as root's authorized_keys.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

KEY_DIR="$SCRIPT_DIR/.keys"
KEY_FILE="$KEY_DIR/id_ed25519"

mkdir -p "$KEY_DIR"
if [[ ! -f "$KEY_FILE" ]]; then
  echo ">> Generating test SSH keypair at $KEY_FILE"
  ssh-keygen -t ed25519 -N "" -C "tf-nixos-acctest" -f "$KEY_FILE" >/dev/null
fi

export NIXOS_TEST_PUBKEY="$(cat "$KEY_FILE.pub")"

echo ">> Building NixOS qcow2 image (first run downloads ~1GB of nixpkgs)..."
nix --extra-experimental-features 'nix-command flakes' \
    build .#qcow --impure -o result-image

# make-disk-image places the qcow2 inside the output directory. Resolve and
# print the path so callers can pick it up.
QCOW="$(find -L result-image -type f -name '*.qcow2' | head -1)"
if [[ -z "$QCOW" ]]; then
  echo "Build succeeded but no .qcow2 found under result-image/" >&2
  ls -R result-image/ >&2
  exit 1
fi

echo ">> Image: $QCOW"
