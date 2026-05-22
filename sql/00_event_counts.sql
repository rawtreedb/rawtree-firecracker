-- Replace <RUN_ID> with the run id printed by the rich example.
SELECT
  toString(event_type) AS event_type,
  count() AS events
FROM sandbox_events
WHERE toString(run_id) = '<RUN_ID>'
GROUP BY event_type
ORDER BY event_type;
