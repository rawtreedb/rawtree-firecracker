#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <base-rootfs.ext4> <output-rootfs.ext4>" >&2
  exit 1
fi

BASE_ROOTFS="$1"
OUTPUT_ROOTFS="$2"

if [ ! -f "$BASE_ROOTFS" ]; then
  echo "Base rootfs does not exist: $BASE_ROOTFS" >&2
  exit 1
fi

cp "$BASE_ROOTFS" "$OUTPUT_ROOTFS"

MOUNT_DIR="$(mktemp -d /tmp/rawtree-rich-rootfs.XXXXXX)"
cleanup() {
  if mountpoint -q "$MOUNT_DIR"; then
    umount "$MOUNT_DIR"
  fi
  rmdir "$MOUNT_DIR"
}
trap cleanup EXIT

mount -o loop "$OUTPUT_ROOTFS" "$MOUNT_DIR"

install -d -m 0755 "$MOUNT_DIR/usr/local/bin"
cat > "$MOUNT_DIR/usr/local/bin/rawtree-rich-workload.sh" <<'WORKLOAD'
#!/bin/sh
set -eu

PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
WORK_DIR=/var/tmp/rawtree-rich-workload
LOG_FILE=/var/log/rawtree-rich-workload.log

log() {
  message="rawtree-rich-workload $(date -Is) $*"
  echo "$message" >> "$LOG_FILE"
  echo "$message" > /dev/ttyS0 2>/dev/null || true
}

mkdir -p "$WORK_DIR"
log "started"

round=1
while [ "$round" -le 6 ]; do
  file="$WORK_DIR/blob-$round.bin"
  log "round=$round write_start"
  dd if=/dev/urandom of="$file" bs=1M count=32 status=none conv=fsync
  sync

  log "round=$round read_start"
  dd if="$file" of=/dev/null bs=1M status=none

  log "round=$round cpu_start"
  timeout 3s sh -c "while :; do sha256sum \"$file\" >/dev/null; done" &
  timeout 3s sh -c "while :; do openssl dgst -sha256 \"$file\" >/dev/null; done" &
  wait || true

  rm -f "$file"
  sync
  log "round=$round complete"
  round=$((round + 1))
  sleep 1
done

log "complete"
WORKLOAD
chmod 0755 "$MOUNT_DIR/usr/local/bin/rawtree-rich-workload.sh"

cat > "$MOUNT_DIR/etc/systemd/system/rawtree-rich-workload.service" <<'UNIT'
[Unit]
Description=RawTree rich Firecracker workload
After=multi-user.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/rawtree-rich-workload.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT

install -d -m 0755 "$MOUNT_DIR/etc/systemd/system/multi-user.target.wants"
ln -sfn ../rawtree-rich-workload.service \
  "$MOUNT_DIR/etc/systemd/system/multi-user.target.wants/rawtree-rich-workload.service"

sync
echo "Prepared rich workload rootfs: $OUTPUT_ROOTFS"
