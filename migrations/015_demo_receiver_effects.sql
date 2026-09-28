CREATE TABLE demo_receiver_effects (
    delivery_id UUID PRIMARY KEY,
    key_id TEXT NOT NULL,
    event_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload_sha256 BYTEA NOT NULL CHECK (octet_length(payload_sha256) = 32),
    applied_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
)
