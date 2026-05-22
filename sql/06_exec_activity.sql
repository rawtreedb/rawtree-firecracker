-- Replace <RUN_ID> with the run id printed by the rich example.
SELECT
  parseDateTime64BestEffort(toString(event_time), 3) AS ts,
  toString(event_type) AS event_type,
  toString(status) AS status,
  toString(exec_id) AS exec_id,
  toString(command) AS command,
  toString(workdir) AS workdir,
  toString(stream) AS stream,
  toUInt64OrZero(toString(chunk_bytes)) AS chunk_bytes,
  toString(chunk_preview) AS chunk_preview,
  toInt64OrZero(toString(exit_code)) AS exit_code,
  toUInt64OrZero(toString(duration_ms)) AS duration_ms,
  toUInt64OrZero(toString(guest_pid)) AS guest_pid
FROM sandbox_events
WHERE toString(run_id) = '<RUN_ID>'
  AND startsWith(toString(event_type), 'sandbox.exec.')
ORDER BY
  ts,
  event_type,
  stream;
