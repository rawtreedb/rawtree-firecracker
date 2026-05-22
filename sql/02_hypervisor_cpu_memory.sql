-- Replace <RUN_ID> with the run id printed by the rich example.
WITH raw AS (
  SELECT
    parseDateTime64BestEffort(toString(sampled_at), 3) AS ts,
    toUnixTimestamp64Milli(ts) AS ts_ms,
    toUInt64OrZero(toString(`hypervisor.pid`)) AS firecracker_pid,
    toUInt64OrZero(toString(`hypervisor.process.status.vm_rss_bytes`)) AS process_rss_bytes,
    toUInt64OrZero(toString(`hypervisor.process.status.vm_hwm_bytes`)) AS process_peak_rss_bytes,
    toUInt64OrZero(toString(`hypervisor.process.stat.cpu_total_ticks`)) AS process_cpu_ticks,
    toUInt64OrZero(toString(`hypervisor.process.fd_count`)) AS process_fd_count,
    toUInt64OrZero(toString(`hypervisor.cgroup.memory_current_bytes`)) AS cgroup_memory_current_bytes,
    toUInt64OrZero(toString(`hypervisor.cgroup.memory_peak_bytes`)) AS cgroup_memory_peak_bytes,
    toUInt64OrZero(toString(`hypervisor.cgroup.cpu_stat.usage_usec`)) AS cgroup_cpu_usage_usec
  FROM sandbox_events
  WHERE toString(run_id) = '<RUN_ID>'
    AND toString(event_type) = 'sandbox.hypervisor.sample'
  ORDER BY ts
),
deltas AS (
  SELECT
    *,
    lagInFrame(ts_ms, 1, ts_ms) OVER sample_window AS prev_ts_ms,
    lagInFrame(process_cpu_ticks, 1, process_cpu_ticks) OVER sample_window AS prev_process_cpu_ticks,
    lagInFrame(cgroup_cpu_usage_usec, 1, cgroup_cpu_usage_usec) OVER sample_window AS prev_cgroup_cpu_usage_usec
  FROM raw
  WINDOW sample_window AS (ORDER BY ts ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
)
SELECT
  ts,
  firecracker_pid,
  round(process_rss_bytes / 1048576, 2) AS process_rss_mib,
  round(process_peak_rss_bytes / 1048576, 2) AS process_peak_rss_mib,
  round(cgroup_memory_current_bytes / 1048576, 2) AS sandbox_cgroup_memory_mib,
  round(cgroup_memory_peak_bytes / 1048576, 2) AS sandbox_cgroup_peak_memory_mib,
  process_fd_count,
  if(ts_ms <= prev_ts_ms, NULL, round(((process_cpu_ticks - prev_process_cpu_ticks) / 100) / ((ts_ms - prev_ts_ms) / 1000) * 100, 2)) AS process_cpu_percent,
  if(ts_ms <= prev_ts_ms, NULL, round(((cgroup_cpu_usage_usec - prev_cgroup_cpu_usage_usec) / 1000000) / ((ts_ms - prev_ts_ms) / 1000) * 100, 2)) AS sandbox_cgroup_cpu_percent
FROM deltas
ORDER BY ts;
