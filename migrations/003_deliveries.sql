CREATE TABLE deliveries (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES events(id),
    endpoint_id UUID NOT NULL REFERENCES endpoints(id),
    endpoint_url TEXT NOT NULL,
    endpoint_version INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status = 'pending'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (event_id, endpoint_id)
)
