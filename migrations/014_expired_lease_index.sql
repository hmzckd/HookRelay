CREATE INDEX deliveries_expired_lease_idx ON deliveries (lease_until, id) WHERE status = 'processing'
