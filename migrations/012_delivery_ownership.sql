ALTER TABLE deliveries
    ADD COLUMN claim_token UUID,
    ADD COLUMN lease_until TIMESTAMPTZ,
    ADD CONSTRAINT deliveries_claim_fields_check
        CHECK ((claim_token IS NULL) = (lease_until IS NULL)),
    ADD CONSTRAINT deliveries_claim_status_check
        CHECK (status = 'processing' OR claim_token IS NULL)
