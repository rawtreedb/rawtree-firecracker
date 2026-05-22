#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_KEY="${RAWTREE_API_KEY:-${RAWTREE_TOKEN:-}}"

if [ -z "$API_KEY" ]; then
  echo "Set RAWTREE_API_KEY before running the rich example." >&2
  exit 1
fi

if [ ! -e /dev/kvm ]; then
  echo "This example must run on a Linux host with /dev/kvm." >&2
  exit 1
fi

BASE_ROOTFS="${BASE_ROOTFS:-/var/lib/firecracker/rootfs.ext4}"
KERNEL="${KERNEL:-/var/lib/firecracker/vmlinux}"
FIRECRACKER="${FIRECRACKER:-/usr/local/bin/firecracker}"
GO_BIN="${GO_BIN:-$(command -v go)}"
TABLE="${RAWTREE_SANDBOX_TABLE:-sandbox_events}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
CGROUP_PATH="${CGROUP_PATH:-rawtree-rich-${STAMP}}"
SANDBOX_ID=""
RUN_ID=""

run_cli() {
  if [ "$(id -u)" -eq 0 ]; then
    env RAWTREE_API_KEY="$API_KEY" "$@"
  else
    sudo -E env RAWTREE_API_KEY="$API_KEY" "$@"
  fi
}

cleanup() {
  if [ -n "$SANDBOX_ID" ]; then
    run_cli "$GO_BIN" run . stop --table "$TABLE" "$SANDBOX_ID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

extract_value() {
  local key="$1"
  awk -F= -v key="$key" '$1 == key { print $2 }'
}

cd "$REPO_ROOT"

echo "Creating observed Firecracker sandbox"
echo "table=$TABLE"
echo "cgroup_path=$CGROUP_PATH"

CREATE_OUTPUT="$(
  run_cli "$GO_BIN" run . create \
    --firecracker "$FIRECRACKER" \
    --kernel "$KERNEL" \
    --rootfs "$BASE_ROOTFS" \
    --table "$TABLE" \
    --vcpus 2 \
    --mem-mib 512 \
    --timeout 10m \
    --hypervisor-sample-interval-ms 1000 \
    --metrics-flush-interval-ms 2000 \
    --cgroup-path "$CGROUP_PATH" \
    --runtime node \
    --metadata provider=example \
    --metadata environment=rich-firecracker \
    --metadata workload=exec-cpu-disk \
    --metadata scenario=rich-example
)"

echo "$CREATE_OUTPUT"
SANDBOX_ID="$(printf '%s\n' "$CREATE_OUTPUT" | extract_value sandbox_id)"
RUN_ID="$(printf '%s\n' "$CREATE_OUTPUT" | extract_value run_id)"

if [ -z "$SANDBOX_ID" ] || [ -z "$RUN_ID" ]; then
  echo "Could not parse sandbox_id or run_id from create output." >&2
  exit 1
fi

echo "Running setup command in $SANDBOX_ID"
run_cli "$GO_BIN" run . exec "$SANDBOX_ID" sh -lc 'uname -a; id; mkdir -p /var/tmp/rawtree-rich-workload'

echo "Running env command in $SANDBOX_ID"
run_cli "$GO_BIN" run . exec --env DEBUG=true --workdir /var/tmp "$SANDBOX_ID" sh -lc 'echo "DEBUG=$DEBUG"; pwd'

echo "Running CPU and disk workload in $SANDBOX_ID"
run_cli "$GO_BIN" run . exec "$SANDBOX_ID" sh -lc '
set -eu
WORK_DIR=/var/tmp/rawtree-rich-workload
mkdir -p "$WORK_DIR"

round=1
while [ "$round" -le 6 ]; do
  file="$WORK_DIR/blob-$round.bin"
  echo "round=$round write_start"
  dd if=/dev/urandom of="$file" bs=1M count=24 status=none conv=fsync
  sync

  echo "round=$round read_start"
  dd if="$file" of=/dev/null bs=1M status=none

  echo "round=$round cpu_start"
  timeout 3s sh -c "while :; do sha256sum \"$file\" >/dev/null; done" &
  wait || true

  rm -f "$file"
  sync
  echo "round=$round complete"
  round=$((round + 1))
  sleep 1
done
'

echo "Stopping $SANDBOX_ID"
run_cli "$GO_BIN" run . stop --table "$TABLE" "$SANDBOX_ID"
SANDBOX_ID=""

echo "run_id=$RUN_ID"
echo "Generate report with:"
echo "  RAWTREE_API_KEY=... node scripts/generate-rich-report.mjs $RUN_ID"
