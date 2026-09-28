CREATE INDEX deliveries_retry_ready_idx ON deliveries (next_attempt_at, id) WHERE status = 'retry_wait'
