#!/usr/bin/env bash
# Boot the NixOS test VM in QEMU.
#
# - Copies the immutable qcow2 base image to a writable overlay (overlay.qcow2)
#   so multiple boots start from a clean state without rebuilding.
# - Forwards a free host port to the VM's port 22.
# - Daemonizes QEMU; PID is recorded in qemu.pid for stop.sh.
# - Waits for sshd, then prints HOST:PORT on stdout for the test harness.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

KEY_FILE="$SCRIPT_DIR/.keys/id_ed25519"
PID_FILE="$SCRIPT_DIR/qemu.pid"
OVERLAY="$SCRIPT_DIR/overlay.qcow2"
SERIAL_LOG="$SCRIPT_DIR/serial.log"

if [[ ! -f "$KEY_FILE" ]]; then
  echo "Test key missing at $KEY_FILE; run ./build.sh first." >&2
  exit 1
fi

BASE_IMAGE="$(find -L "$SCRIPT_DIR/result-image" -type f -name '*.qcow2' | head -1)"
if [[ -z "$BASE_IMAGE" ]]; then
  echo "Base qcow2 not found; run ./build.sh first." >&2
  exit 1
fi

# Stop any existing VM.
if [[ -f "$PID_FILE" ]]; then
  kill "$(cat "$PID_FILE")" 2>/dev/null || true
  rm -f "$PID_FILE"
fi
rm -f "$OVERLAY"

# Build a copy-on-write overlay so the base image stays pristine and reboots
# are fast.
qemu-img create -f qcow2 -F qcow2 -b "$BASE_IMAGE" "$OVERLAY" >/dev/null

# Fixed host port that matches the guest-side alternate sshd listener (see
# flake.nix services.openssh.ports). Using a fixed port lets the build_host
# scenario use the same address from both host- and guest-loopback views; if
# something else on the host is already bound to 22222 the QEMU launch fails
# with a clear error.
PORT=22222

KVM_FLAGS=()
if [[ -e /dev/kvm && -r /dev/kvm && -w /dev/kvm ]]; then
  KVM_FLAGS=(-enable-kvm -cpu host)
else
  echo ">> /dev/kvm not accessible; falling back to TCG (slow)" >&2
  KVM_FLAGS=(-cpu max)
fi

qemu-system-x86_64 \
  "${KVM_FLAGS[@]}" \
  -m 2048 \
  -smp 2 \
  -display none \
  -serial "file:$SERIAL_LOG" \
  -monitor none \
  -drive "file=$OVERLAY,if=virtio,format=qcow2" \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$PORT-:$PORT" \
  -device virtio-net-pci,netdev=net0 \
  -daemonize \
  -pidfile "$PID_FILE"

# Wait for sshd. Boot + activation can be ~30s under KVM, longer under TCG.
TIMEOUT="${NIXOS_TEST_BOOT_TIMEOUT:-180}"
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

echo "VM did not become reachable on 127.0.0.1:$PORT in ${TIMEOUT}s" >&2
echo "--- last 80 lines of serial log ---" >&2
tail -80 "$SERIAL_LOG" >&2 || true
exit 1
