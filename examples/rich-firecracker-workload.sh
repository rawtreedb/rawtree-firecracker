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
RICH_ROOTFS="${RICH_ROOTFS:-/var/lib/firecracker/rootfs-rich.ext4}"
KERNEL="${KERNEL:-/var/lib/firecracker/vmlinux}"
FIRECRACKER="${FIRECRACKER:-/usr/local/bin/firecracker}"
TABLE="${RAWTREE_SANDBOX_TABLE:-sandbox_events}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
CGROUP_PATH="${CGROUP_PATH:-rawtree-rich-${STAMP}}"

run_as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    sudo "$@"
  fi
}

cd "$REPO_ROOT"
run_as_root "$REPO_ROOT/scripts/prepare-rich-rootfs.sh" "$BASE_ROOTFS" "$RICH_ROOTFS"

echo "Starting rich Firecracker workload"
echo "table=$TABLE"
echo "cgroup_path=$CGROUP_PATH"

if [ "$(id -u)" -eq 0 ]; then
  env RAWTREE_API_KEY="$API_KEY" npm run start -- \
    --firecracker "$FIRECRACKER" \
    --kernel "$KERNEL" \
    --rootfs "$RICH_ROOTFS" \
    --table "$TABLE" \
    --vcpu-count 2 \
    --mem-mib 512 \
    --run-timeout-ms 45000 \
    --hypervisor-sample-interval-ms 1000 \
    --metrics-flush-interval-ms 2000 \
    --cgroup-path "$CGROUP_PATH" \
    --metadata provider=example \
    --metadata environment=rich-firecracker \
    --metadata workload=cpu-disk \
    --metadata scenario=rich-example
else
  sudo -E env RAWTREE_API_KEY="$API_KEY" npm run start -- \
    --firecracker "$FIRECRACKER" \
    --kernel "$KERNEL" \
    --rootfs "$RICH_ROOTFS" \
    --table "$TABLE" \
    --vcpu-count 2 \
    --mem-mib 512 \
    --run-timeout-ms 45000 \
    --hypervisor-sample-interval-ms 1000 \
    --metrics-flush-interval-ms 2000 \
    --cgroup-path "$CGROUP_PATH" \
    --metadata provider=example \
    --metadata environment=rich-firecracker \
    --metadata workload=cpu-disk \
    --metadata scenario=rich-example
fi
