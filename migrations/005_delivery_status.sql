ALTER TABLE deliveries
    DROP CONSTRAINT deliveries_status_check,
    ADD CONSTRAINT deliveries_status_check
        CHECK (status IN ('pending', 'processing', 'succeeded', 'failed'))
