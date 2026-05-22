-- Replace <RUN_ID> with the run id printed by the rich example.
SELECT
  parseDateTime64BestEffort(toString(sampled_at), 3) AS ts,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.read_bytes`)) AS rootfs_read_bytes,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.write_bytes`)) AS rootfs_write_bytes,
  round(rootfs_read_bytes / 1048576, 2) AS rootfs_read_mib,
  round(rootfs_write_bytes / 1048576, 2) AS rootfs_write_mib,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.read_count`)) AS rootfs_read_count,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.write_count`)) AS rootfs_write_count,
  toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_io_in`))
    + toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_io_out`))
    + toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_mmio_read`))
    + toUInt64OrZero(toString(`firecracker.metrics.vcpu.exit_mmio_write`)) AS vcpu_exits,
  toUInt64OrZero(toString(`firecracker.metrics.uart.write_count`)) AS uart_writes
FROM sandbox_events
WHERE toString(run_id) = '<RUN_ID>'
  AND toString(event_type) = 'sandbox.firecracker.vmm.metrics'
ORDER BY ts;
