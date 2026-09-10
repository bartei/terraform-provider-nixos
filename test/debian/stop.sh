#!/usr/bin/env bash
# Stop the Debian test VM and remove its overlay disk and seed ISO.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="$SCRIPT_DIR/qemu.pid"

if [[ -f "$PID_FILE" ]]; then
  kill "$(cat "$PID_FILE")" 2>/dev/null || true
  rm -f "$PID_FILE"
fi
rm -f "$SCRIPT_DIR/overlay.qcow2" "$SCRIPT_DIR/seed.iso"
