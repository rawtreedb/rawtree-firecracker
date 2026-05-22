-- Replace <RUN_ID> with the run id printed by the rich example.
SELECT
  parseDateTime64BestEffort(toString(sampled_at), 3) AS ts,
  toString(`firecracker.log.line`) AS log_line
FROM sandbox_events
WHERE toString(run_id) = '<RUN_ID>'
  AND toString(event_type) = 'sandbox.firecracker.vmm.log'
ORDER BY ts;
