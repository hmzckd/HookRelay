package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"hookrelay/internal/delivery"
	"hookrelay/internal/events"
)

const claimLease = 15 * time.Second

var ErrClaimLost = delivery.ErrClaimLost

func (s EventStore) Claim(ctx context.Context) (*delivery.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback()
	// Expire jobs that outlived their age budget while the worker was stopped.
	if _, err := tx.ExecContext(ctx, `WITH expired AS (
		SELECT id FROM deliveries
		WHERE status IN ('pending', 'retry_wait')
		AND created_at <= clock_timestamp() - ($1::bigint * interval '1 second')
		ORDER BY created_at, id LIMIT 100 FOR UPDATE SKIP LOCKED
	)
	UPDATE deliveries d
	SET status = 'dead', next_attempt_at = NULL, terminal_reason = 'age_exhausted'
	FROM expired WHERE d.id = expired.id`,
		int64(delivery.MaxAge/time.Second)); err != nil {
		return nil, fmt.Errorf("expire old deliveries: %w", err)
	}
	job := &delivery.Job{}
	var status string
	var attempts int
	var createdAt time.Time
	var endpointEnabled bool
	err = tx.QueryRowContext(ctx, `SELECT d.id, d.event_id, e.event_type, e.payload,
		d.endpoint_id, d.endpoint_url, d.secret_ref, d.status, d.attempt_count, d.created_at, ep.enabled
		FROM deliveries d
		JOIN events e ON e.id = d.event_id
		JOIN endpoints ep ON ep.id = d.endpoint_id
		WHERE (ep.enabled = TRUE AND (d.status = 'pending' OR (d.status = 'retry_wait' AND d.next_attempt_at <= clock_timestamp()))
			AND d.attempt_count < $1
			AND d.created_at > clock_timestamp() - ($2::bigint * interval '1 second'))
		OR (d.status = 'processing' AND (d.lease_until IS NULL OR d.lease_until <= clock_timestamp()))
		ORDER BY COALESCE(d.next_attempt_at, d.lease_until, d.created_at), d.id
		LIMIT 1 FOR UPDATE OF d SKIP LOCKED`,
		delivery.MaxAttempts, int64(delivery.MaxAge/time.Second)).
		Scan(&job.ID, &job.EventID, &job.EventType, &job.Payload, &job.EndpointID, &job.EndpointURL, &job.SecretRef,
			&status, &attempts, &createdAt, &endpointEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit expired deliveries: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find pending delivery: %w", err)
	}
	if status == "processing" {
		updated, err := tx.ExecContext(ctx, `UPDATE delivery_attempts
			SET status = 'unknown', finished_at = clock_timestamp(), failure_class = 'ownership_lost'
			WHERE delivery_id = $1 AND status = 'processing'`, job.ID)
		if err != nil {
			return nil, fmt.Errorf("close abandoned attempt: %w", err)
		}
		if count, err := updated.RowsAffected(); err != nil || count != 1 {
			return nil, errors.New("abandoned delivery does not have exactly one processing attempt")
		}
		if !endpointEnabled {
			if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET status = 'dead', next_attempt_at = NULL,
				terminal_reason = 'endpoint_disabled', claim_token = NULL, lease_until = NULL WHERE id = $1`, job.ID); err != nil {
				return nil, fmt.Errorf("stop abandoned disabled delivery: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit disabled delivery: %w", err)
			}
			return nil, nil
		}
	}
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return nil, fmt.Errorf("read database clock: %w", err)
	}
	var terminalReason string
	if attempts >= delivery.MaxAttempts {
		terminalReason = "max_attempts"
	} else if !databaseNow.Before(createdAt.Add(delivery.MaxAge)) {
		terminalReason = "age_exhausted"
	}
	if terminalReason != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries
			SET status = 'dead', next_attempt_at = NULL, terminal_reason = $2,
				claim_token = NULL, lease_until = NULL WHERE id = $1`, job.ID, terminalReason); err != nil {
			return nil, fmt.Errorf("finish exhausted delivery: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit exhausted delivery: %w", err)
		}
		return nil, nil
	}
	job.AttemptID, err = events.NewID()
	if err != nil {
		return nil, err
	}
	job.ClaimToken, err = events.NewID()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deliveries
		SET status = 'processing', attempt_count = attempt_count + 1, next_attempt_at = NULL,
			claim_token = $2, lease_until = clock_timestamp() + ($3::bigint * interval '1 millisecond')
		WHERE id = $1`, job.ID, job.ClaimToken, int64(claimLease/time.Millisecond)); err != nil {
		return nil, fmt.Errorf("mark delivery processing: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_attempts (id, delivery_id, status)
		VALUES ($1, $2, 'processing')`, job.AttemptID, job.ID); err != nil {
		return nil, fmt.Errorf("start delivery attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return job, nil
}

func (s EventStore) Complete(ctx context.Context, job delivery.Job, result delivery.Result) error {
	if result.Status != "succeeded" && result.Status != "failed" {
		return errors.New("invalid delivery result")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin completion: %w", err)
	}
	defer tx.Rollback()
	var createdAt time.Time
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT created_at, attempt_count FROM deliveries
		WHERE id = $1 AND status = 'processing' AND claim_token = $2
		AND lease_until > clock_timestamp() FOR UPDATE`, job.ID, job.ClaimToken).
		Scan(&createdAt, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClaimLost
	}
	if err != nil {
		return fmt.Errorf("read owned delivery: %w", err)
	}
	update, err := tx.ExecContext(ctx, `UPDATE delivery_attempts
		SET status = $1, finished_at = clock_timestamp(), http_status = $2, failure_class = NULLIF($3, '')
		WHERE id = $4 AND delivery_id = $5 AND status = 'processing'`,
		result.Status, result.HTTPStatus, result.FailureClass, job.AttemptID, job.ID)
	if err != nil {
		return fmt.Errorf("finish attempt: %w", err)
	}
	if count, err := update.RowsAffected(); err != nil || count != 1 {
		return errors.New("attempt is not processing")
	}
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return fmt.Errorf("read database clock: %w", err)
	}
	decision := (delivery.RetryPolicy{Now: func() time.Time { return databaseNow }, Jitter: s.Jitter}).Decide(result, attempts, createdAt)
	var nextAttempt, terminalReason any
	if decision.Status == "retry_wait" {
		nextAttempt = decision.NextAttemptAt
	}
	if decision.Status == "dead" {
		terminalReason = decision.Reason
	}
	update, err = tx.ExecContext(ctx, `UPDATE deliveries
		SET status = $1, next_attempt_at = $2, terminal_reason = $3,
			claim_token = NULL, lease_until = NULL
		WHERE id = $4 AND status = 'processing' AND claim_token = $5
		AND lease_until > clock_timestamp()`, decision.Status, nextAttempt, terminalReason, job.ID, job.ClaimToken)
	if err != nil {
		return fmt.Errorf("finish delivery: %w", err)
	}
	if count, err := update.RowsAffected(); err != nil || count != 1 {
		return ErrClaimLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completion: %w", err)
	}
	slog.Info("delivery outcome persisted", "event_id", job.EventID, "delivery_id", job.ID,
		"attempt_id", job.AttemptID, "endpoint_id", job.EndpointID,
		"delivery_status", decision.Status, "terminal_reason", decision.Reason,
		"attempt_count", attempts)
	return nil
}

func (s EventStore) GetDelivery(ctx context.Context, id string) (events.Delivery, error) {
	var item events.Delivery
	var nextAttempt sql.NullTime
	var terminalReason sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id, event_id, endpoint_id, endpoint_url,
		endpoint_version, status, attempt_count, next_attempt_at, terminal_reason, created_at
		FROM deliveries WHERE id = $1`, id).
		Scan(&item.ID, &item.EventID, &item.EndpointID, &item.EndpointURL,
			&item.EndpointVersion, &item.Status, &item.AttemptCount, &nextAttempt, &terminalReason, &item.CreatedAt)
	if nextAttempt.Valid {
		item.NextAttemptAt = &nextAttempt.Time
	}
	if terminalReason.Valid {
		item.TerminalReason = terminalReason.String
	}
	return item, err
}

func (s EventStore) ListAttempts(ctx context.Context, deliveryID string, limit, offset int) (events.AttemptPage, error) {
	page := events.AttemptPage{Items: []events.Attempt{}, Limit: limit, Offset: offset}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, delivery_id, status, started_at, finished_at,
		http_status, failure_class FROM delivery_attempts WHERE delivery_id = $1
		ORDER BY started_at DESC, id DESC LIMIT $2 OFFSET $3`, deliveryID, limit+1, offset)
	if err != nil {
		return events.AttemptPage{}, fmt.Errorf("query delivery attempts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item events.Attempt
		var finished sql.NullTime
		var httpStatus sql.NullInt64
		var failureClass sql.NullString
		if err := rows.Scan(&item.ID, &item.DeliveryID, &item.Status, &item.StartedAt,
			&finished, &httpStatus, &failureClass); err != nil {
			return events.AttemptPage{}, fmt.Errorf("scan delivery attempt: %w", err)
		}
		if finished.Valid {
			item.FinishedAt = &finished.Time
		}
		if httpStatus.Valid {
			status := int(httpStatus.Int64)
			item.HTTPStatus = &status
		}
		if failureClass.Valid {
			item.FailureClass = failureClass.String
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return events.AttemptPage{}, fmt.Errorf("read delivery attempts: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		next := offset + limit
		page.NextOffset = &next
	}
	return page, nil
}
