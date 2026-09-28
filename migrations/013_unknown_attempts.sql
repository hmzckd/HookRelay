ALTER TABLE delivery_attempts
    DROP CONSTRAINT delivery_attempts_status_check,
    DROP CONSTRAINT delivery_attempts_check,
    ADD CONSTRAINT delivery_attempts_status_check
        CHECK (status IN ('processing', 'succeeded', 'failed', 'unknown')),
    ADD CONSTRAINT delivery_attempts_check
        CHECK (
            (status = 'processing' AND finished_at IS NULL)
            OR (status IN ('succeeded', 'failed', 'unknown') AND finished_at IS NOT NULL)
        )
