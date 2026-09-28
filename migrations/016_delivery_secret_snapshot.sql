DO $$
BEGIN
    ALTER TABLE deliveries ADD COLUMN secret_ref TEXT;
    UPDATE deliveries d SET secret_ref = e.secret_ref FROM endpoints e WHERE e.id = d.endpoint_id;
    ALTER TABLE deliveries ALTER COLUMN secret_ref SET NOT NULL;
END $$
