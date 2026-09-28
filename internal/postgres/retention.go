package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const RetentionDays = 30
const MaxCleanupBatch = 1000

const expiredEventCondition = `e.created_at < clock_timestamp() - interval '30 days'
	AND NOT EXISTS (
		SELECT 1 FROM deliveries d WHERE d.event_id = e.id
		AND d.status NOT IN ('succeeded', 'failed', 'dead')
	)
	AND NOT EXISTS (
		SELECT 1 FROM idempotency_keys k WHERE k.event_id = e.id
		AND k.created_at >= clock_timestamp() - interval '30 days'
	)
	AND NOT EXISTS (
		SELECT 1 FROM delivery_attempts a JOIN deliveries d ON d.id = a.delivery_id
		WHERE d.event_id = e.id
		AND coalesce(a.finished_at, a.started_at) >= clock_timestamp() - interval '30 days'
	)
	AND NOT EXISTS (
		SELECT 1 FROM demo_receiver_effects r JOIN deliveries d ON d.id = r.delivery_id
		WHERE d.event_id = e.id
		AND r.applied_at >= clock_timestamp() - interval '30 days'
	)`

// CountExpired is a read-only preview. The result can change before cleanup.
func (s EventStore) CountExpired(ctx context.Context) (int64, error) {
	var count int64
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM events e WHERE `+expiredEventCondition).Scan(&count); err != nil {
		return 0, fmt.Errorf("count expired events: %w", err)
	}
	return count, nil
}

// CleanupExpired removes at most limit eligible events and all of their child
// records in one transaction. It never removes an event with unfinished work.
func (s EventStore) CleanupExpired(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > MaxCleanupBatch {
		return 0, fmt.Errorf("cleanup limit must be between 1 and %d", MaxCleanupBatch)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin retention cleanup: %w", err)
	}
	defer tx.Rollback()
	removed := 0
	for removed < limit {
		var eventID string
		err := tx.QueryRowContext(ctx, `SELECT e.id FROM events e WHERE `+expiredEventCondition+`
			ORDER BY e.created_at, e.id LIMIT 1 FOR UPDATE OF e SKIP LOCKED`).Scan(&eventID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("select expired event: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE event_id = $1`, eventID); err != nil {
			return 0, fmt.Errorf("remove expired idempotency key: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM demo_receiver_effects
			WHERE delivery_id IN (SELECT id FROM deliveries WHERE event_id = $1)`, eventID); err != nil {
			return 0, fmt.Errorf("remove expired demo effects: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM delivery_attempts
			WHERE delivery_id IN (SELECT id FROM deliveries WHERE event_id = $1)`, eventID); err != nil {
			return 0, fmt.Errorf("remove expired attempts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE event_id = $1`, eventID); err != nil {
			return 0, fmt.Errorf("remove expired deliveries: %w", err)
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id = $1`, eventID)
		if err != nil {
			return 0, fmt.Errorf("remove expired event: %w", err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return 0, errors.New("expired event was not deleted exactly once")
		}
		removed++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit retention cleanup: %w", err)
	}
	return removed, nil
}
