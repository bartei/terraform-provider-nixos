#!/usr/bin/env bash
# Boot a stock Debian cloud image in QEMU as the target for the
# nixos_system_manager acceptance tests (a non-NixOS host).
#
# - Downloads the Debian genericcloud qcow2 once (cached as base.qcow2).
# - Builds a cloud-init NoCloud seed ISO that authorizes the shared test key
#   (test/qemu/.keys/id_ed25519, generated if missing) for root.
# - Boots from a copy-on-write overlay so every run starts clean.
# - Waits for sshd, then prints HOST:PORT on stdout for the test harness.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

KEY_DIR="$SCRIPT_DIR/../qemu/.keys"
KEY_FILE="$KEY_DIR/id_ed25519"
BASE_IMAGE="$SCRIPT_DIR/base.qcow2"
OVERLAY="$SCRIPT_DIR/overlay.qcow2"
SEED_ISO="$SCRIPT_DIR/seed.iso"
PID_FILE="$SCRIPT_DIR/qemu.pid"
SERIAL_LOG="$SCRIPT_DIR/serial.log"
IMAGE_URL="${DEBIAN_TEST_IMAGE_URL:-https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2}"
# Distinct from the NixOS VM's 22222 so both can run side by side.
PORT="${DEBIAN_TEST_PORT:-22223}"

mkdir -p "$KEY_DIR"
if [[ ! -f "$KEY_FILE" ]]; then
  echo ">> Generating test SSH keypair at $KEY_FILE" >&2
  ssh-keygen -t ed25519 -N "" -C "tf-nixos-acctest" -f "$KEY_FILE" >/dev/null
fi

if [[ ! -f "$BASE_IMAGE" ]]; then
  echo ">> Downloading $IMAGE_URL" >&2
  curl -fsSL -o "$BASE_IMAGE.part" "$IMAGE_URL"
  mv "$BASE_IMAGE.part" "$BASE_IMAGE"
fi

# Stop any existing VM.
if [[ -f "$PID_FILE" ]]; then
  kill "$(cat "$PID_FILE")" 2>/dev/null || true
  rm -f "$PID_FILE"
fi
rm -f "$OVERLAY" "$SEED_ISO"

# cloud-init NoCloud seed: enable root key login. The stock image sets
# disable_root: true, which would replace root's authorized_keys with a
# "please login as debian" stub.
SEED_DIR="$(mktemp -d)"
trap 'rm -rf "$SEED_DIR"' EXIT
cat > "$SEED_DIR/meta-data" <<META
instance-id: tf-nixos-sysmgr-test
local-hostname: debian-test
META
cat > "$SEED_DIR/user-data" <<USER
#cloud-config
disable_root: false
ssh_pwauth: false
users:
  - name: root
    ssh_authorized_keys:
      - $(cat "$KEY_FILE.pub")
USER
genisoimage -quiet -output "$SEED_ISO" -volid cidata -joliet -rock \
  "$SEED_DIR/user-data" "$SEED_DIR/meta-data"

qemu-img create -q -f qcow2 -F qcow2 -b "$BASE_IMAGE" "$OVERLAY" 20G

KVM_FLAGS=()
if [[ -e /dev/kvm && -r /dev/kvm && -w /dev/kvm ]]; then
  KVM_FLAGS=(-enable-kvm -cpu host)
else
  echo ">> /dev/kvm not accessible; falling back to TCG (slow)" >&2
  KVM_FLAGS=(-cpu max)
fi

qemu-system-x86_64 \
  "${KVM_FLAGS[@]}" \
  -m 4096 \
  -smp 4 \
  -display none \
  -serial "file:$SERIAL_LOG" \
  -monitor none \
  -drive "file=$OVERLAY,if=virtio,format=qcow2" \
  -drive "file=$SEED_ISO,if=virtio,format=raw,readonly=on" \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$PORT-:22" \
  -device virtio-net-pci,netdev=net0 \
  -daemonize \
  -pidfile "$PID_FILE"

TIMEOUT="${DEBIAN_TEST_BOOT_TIMEOUT:-240}"
for _ in $(seq 1 "$TIMEOUT"); do
  if ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
       -o ConnectTimeout=2 -o BatchMode=yes -o IdentitiesOnly=yes \
       -i "$KEY_FILE" -p "$PORT" \
       root@127.0.0.1 true 2>/dev/null; then
    echo "127.0.0.1:$PORT"
    exit 0
  fi
  sleep 1
done

echo "Debian VM did not become reachable on 127.0.0.1:$PORT in ${TIMEOUT}s" >&2
echo "--- last 80 lines of serial log ---" >&2
tail -80 "$SERIAL_LOG" >&2 || true
exit 1
