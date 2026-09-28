ALTER TABLE deliveries
    DROP CONSTRAINT deliveries_status_check,
    ADD CONSTRAINT deliveries_status_check
        CHECK (status IN ('pending', 'processing', 'succeeded', 'failed', 'retry_wait', 'dead')),
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 5),
    ADD COLUMN next_attempt_at TIMESTAMPTZ,
    ADD COLUMN terminal_reason TEXT,
    ADD CONSTRAINT deliveries_retry_time_check
        CHECK ((status = 'retry_wait') = (next_attempt_at IS NOT NULL)),
    ADD CONSTRAINT deliveries_dead_reason_check
        CHECK ((status = 'dead') = (terminal_reason IS NOT NULL))
