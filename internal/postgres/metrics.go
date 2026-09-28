package postgres

import (
	"context"
	"fmt"

	"hookrelay/internal/telemetry"
)

func (s EventStore) Metrics(ctx context.Context) (telemetry.Snapshot, error) {
	var snapshot telemetry.Snapshot
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&snapshot.EventsStored); err != nil {
		return telemetry.Snapshot{}, fmt.Errorf("count stored events: %w", err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE status = 'pending'),
		count(*) FILTER (WHERE status = 'processing'),
		count(*) FILTER (WHERE status = 'retry_wait'),
		count(*) FILTER (WHERE status = 'succeeded'),
		count(*) FILTER (WHERE status = 'failed'),
		count(*) FILTER (WHERE status = 'dead'),
		coalesce(sum(greatest(attempt_count - 1, 0)), 0),
		count(*) FILTER (WHERE status = 'pending' OR (status = 'retry_wait' AND next_attempt_at <= clock_timestamp())),
		coalesce(max(greatest(0, extract(epoch FROM clock_timestamp() - created_at)))
			FILTER (WHERE status IN ('pending', 'retry_wait')), 0)::double precision
		FROM deliveries`).Scan(
		&snapshot.Deliveries[0], &snapshot.Deliveries[1], &snapshot.Deliveries[2],
		&snapshot.Deliveries[3], &snapshot.Deliveries[4], &snapshot.Deliveries[5],
		&snapshot.RetriesStarted, &snapshot.QueueReady, &snapshot.QueueOldestAgeSeconds,
	); err != nil {
		return telemetry.Snapshot{}, fmt.Errorf("measure delivery queue: %w", err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE status = 'processing'),
		count(*) FILTER (WHERE status = 'succeeded'),
		count(*) FILTER (WHERE status = 'failed'),
		count(*) FILTER (WHERE status = 'unknown'),
		count(*) FILTER (WHERE status IN ('succeeded', 'failed') AND finished_at - started_at <= interval '0.1 second'),
		count(*) FILTER (WHERE status IN ('succeeded', 'failed') AND finished_at - started_at <= interval '0.5 second'),
		count(*) FILTER (WHERE status IN ('succeeded', 'failed') AND finished_at - started_at <= interval '1 second'),
		count(*) FILTER (WHERE status IN ('succeeded', 'failed') AND finished_at - started_at <= interval '2 seconds'),
		count(*) FILTER (WHERE status IN ('succeeded', 'failed') AND finished_at - started_at <= interval '5 seconds'),
		count(*) FILTER (WHERE status IN ('succeeded', 'failed') AND finished_at IS NOT NULL),
		coalesce(sum(greatest(0, extract(epoch FROM finished_at - started_at)))
			FILTER (WHERE status IN ('succeeded', 'failed') AND finished_at IS NOT NULL), 0)::double precision
		FROM delivery_attempts`).Scan(
		&snapshot.Attempts[0], &snapshot.Attempts[1], &snapshot.Attempts[2], &snapshot.Attempts[3],
		&snapshot.AttemptDurationBuckets[0], &snapshot.AttemptDurationBuckets[1],
		&snapshot.AttemptDurationBuckets[2], &snapshot.AttemptDurationBuckets[3],
		&snapshot.AttemptDurationBuckets[4], &snapshot.AttemptDurationCount,
		&snapshot.AttemptDurationSumSeconds,
	); err != nil {
		return telemetry.Snapshot{}, fmt.Errorf("measure delivery attempts: %w", err)
	}
	snapshot.DBPool = s.DB.Stats()
	return snapshot, nil
}
