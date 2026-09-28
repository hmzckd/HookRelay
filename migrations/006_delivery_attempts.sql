CREATE TABLE delivery_attempts (
    id UUID PRIMARY KEY,
    delivery_id UUID NOT NULL REFERENCES deliveries(id),
    status TEXT NOT NULL CHECK (status IN ('processing', 'succeeded', 'failed')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    http_status INTEGER CHECK (http_status BETWEEN 100 AND 599),
    failure_class TEXT,
    CHECK (
        (status = 'processing' AND finished_at IS NULL)
        OR (status IN ('succeeded', 'failed') AND finished_at IS NOT NULL)
    )
)
