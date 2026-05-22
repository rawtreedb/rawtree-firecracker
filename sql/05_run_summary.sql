-- Replace <RUN_ID> with the run id printed by the rich example.
WITH
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.read_bytes`)) AS rootfs_read_bytes_named,
  toUInt64OrZero(toString(`firecracker.metrics.block_rootfs.write_bytes`)) AS rootfs_write_bytes_named,
  toUInt64OrZero(toString(`firecracker.metrics.block_root_drive.read_bytes`)) AS rootfs_read_bytes_sdk_default,
  toUInt64OrZero(toString(`firecracker.metrics.block_root_drive.write_bytes`)) AS rootfs_write_bytes_sdk_default,
  toUInt64OrZero(toString(`firecracker.metrics.block.read_bytes`)) AS rootfs_read_bytes_aggregate,
  toUInt64OrZero(toString(`firecracker.metrics.block.write_bytes`)) AS rootfs_write_bytes_aggregate,
  if(rootfs_read_bytes_named > 0, rootfs_read_bytes_named, if(rootfs_read_bytes_sdk_default > 0, rootfs_read_bytes_sdk_default, rootfs_read_bytes_aggregate)) AS rootfs_read_bytes_per_event,
  if(rootfs_write_bytes_named > 0, rootfs_write_bytes_named, if(rootfs_write_bytes_sdk_default > 0, rootfs_write_bytes_sdk_default, rootfs_write_bytes_aggregate)) AS rootfs_write_bytes_per_event
SELECT
  toString(run_id) AS run,
  min(parseDateTime64BestEffort(toString(event_time), 3)) AS started_at,
  max(parseDateTime64BestEffort(toString(event_time), 3)) AS finished_at,
  dateDiff('second', started_at, finished_at) AS duration_seconds,
  count() AS total_events,
  countIf(toString(event_type) = 'sandbox.hypervisor.sample') AS hypervisor_samples,
  countIf(toString(event_type) = 'sandbox.firecracker.vmm.metrics') AS firecracker_metric_samples,
  round(max(toUInt64OrZero(toString(`hypervisor.process.status.vm_rss_bytes`))) / 1048576, 2) AS peak_process_rss_mib,
  round(max(toUInt64OrZero(toString(`hypervisor.cgroup.memory_current_bytes`))) / 1048576, 2) AS peak_sandbox_cgroup_memory_mib,
  sum(rootfs_read_bytes_per_event) AS rootfs_read_bytes,
  sum(rootfs_write_bytes_per_event) AS rootfs_write_bytes
FROM sandbox_events
WHERE toString(run_id) = '<RUN_ID>'
GROUP BY run;
