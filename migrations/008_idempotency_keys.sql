CREATE TABLE idempotency_keys (
    producer_id TEXT NOT NULL CHECK (producer_id <> ''),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    event_id UUID NOT NULL UNIQUE REFERENCES events(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (producer_id, idempotency_key)
)
