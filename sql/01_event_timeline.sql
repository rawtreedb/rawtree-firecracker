-- Replace <RUN_ID> with the run id printed by the rich example.
SELECT
  toStartOfSecond(parseDateTime64BestEffort(toString(event_time), 3)) AS second,
  toString(event_type) AS event_type,
  count() AS events
FROM sandbox_events
WHERE toString(run_id) = '<RUN_ID>'
GROUP BY
  second,
  event_type
ORDER BY
  second,
  event_type;
