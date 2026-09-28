package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	"hookrelay/internal/events"
)

var ErrUnknownEndpoint = errors.New("endpoint does not exist or is disabled")
var ErrIdempotencyConflict = errors.New("idempotency key was used with different event content")

type EventStore struct {
	DB     *sql.DB
	Jitter func(time.Duration) time.Duration
}

func (s EventStore) Create(ctx context.Context, producerID, key string, input events.Input) (events.Event, bool, error) {
	if producerID == "" || key == "" {
		return events.Event{}, false, errors.New("producer and idempotency key are required")
	}
	input.EndpointIDs = append([]string(nil), input.EndpointIDs...)
	sort.Strings(input.EndpointIDs)
	requestHash := events.RequestHash(input)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("begin event transaction: %w", err)
	}
	defer tx.Rollback()

	// The key lock serializes concurrent first uses. The unique database key is
	// still the durable invariant; a rare 64-bit lock collision only delays work.
	lockHash := sha256.Sum256([]byte(producerID + "\x00" + key))
	lockID := int64(binary.BigEndian.Uint64(lockHash[:8]))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockID); err != nil {
		return events.Event{}, false, fmt.Errorf("lock idempotency key: %w", err)
	}
	var existingID string
	var existingHash []byte
	err = tx.QueryRowContext(ctx, `SELECT event_id, request_hash FROM idempotency_keys WHERE producer_id = $1 AND idempotency_key = $2 FOR SHARE`, producerID, key).
		Scan(&existingID, &existingHash)
	if err == nil {
		if subtle.ConstantTimeCompare(existingHash, requestHash[:]) != 1 {
			return events.Event{}, false, ErrIdempotencyConflict
		}
		// Keep the key row locked until the event and its deliveries have been
		// read. Retention must delete the key before deleting the event.
		event, err := getEvent(ctx, tx, existingID)
		if err != nil {
			return events.Event{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return events.Event{}, false, fmt.Errorf("commit idempotency lookup: %w", err)
		}
		return event, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return events.Event{}, false, fmt.Errorf("look up idempotency key: %w", err)
	}

	// Validate all targets before inserting the event. The row locks keep a target
	// from being disabled or changed between validation and the snapshot insert.
	type target struct {
		id, url, secretRef string
		version            int
	}
	targets := make([]target, 0, len(input.EndpointIDs))
	for _, id := range input.EndpointIDs {
		var t target
		t.id = id
		err := tx.QueryRowContext(ctx, `SELECT url, version, secret_ref FROM endpoints WHERE id = $1 AND enabled = TRUE FOR SHARE`, id).Scan(&t.url, &t.version, &t.secretRef)
		if errors.Is(err, sql.ErrNoRows) {
			return events.Event{}, false, ErrUnknownEndpoint
		}
		if err != nil {
			return events.Event{}, false, fmt.Errorf("look up endpoint: %w", err)
		}
		targets = append(targets, t)
	}
	eventID, err := events.NewID()
	if err != nil {
		return events.Event{}, false, err
	}
	event := events.Event{ID: eventID, Type: input.Type, Payload: input.Payload, Deliveries: make([]events.Delivery, 0, len(targets))}
	err = tx.QueryRowContext(ctx, `INSERT INTO events (id, event_type, payload) VALUES ($1, $2, $3) RETURNING created_at`, event.ID, event.Type, []byte(event.Payload)).Scan(&event.CreatedAt)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("insert event: %w", err)
	}
	for _, target := range targets {
		id, err := events.NewID()
		if err != nil {
			return events.Event{}, false, err
		}
		delivery := events.Delivery{ID: id, EndpointID: target.id, EndpointURL: target.url, EndpointVersion: target.version, Status: "pending"}
		err = tx.QueryRowContext(ctx, `INSERT INTO deliveries (id, event_id, endpoint_id, endpoint_url, endpoint_version, secret_ref)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`, id, event.ID, target.id, target.url, target.version, target.secretRef).Scan(&delivery.CreatedAt)
		if err != nil {
			return events.Event{}, false, fmt.Errorf("insert delivery: %w", err)
		}
		event.Deliveries = append(event.Deliveries, delivery)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys (producer_id, idempotency_key, request_hash, event_id)
		VALUES ($1, $2, $3, $4)`, producerID, key, requestHash[:], event.ID); err != nil {
		return events.Event{}, false, fmt.Errorf("insert idempotency key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return events.Event{}, false, fmt.Errorf("commit event: %w", err)
	}
	return event, false, nil
}

func (s EventStore) Get(ctx context.Context, id string) (events.Event, error) {
	return getEvent(ctx, s.DB, id)
}

type eventQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func getEvent(ctx context.Context, q eventQuerier, id string) (events.Event, error) {
	event := events.Event{Deliveries: []events.Delivery{}}
	err := q.QueryRowContext(ctx, `SELECT id, event_type, payload, created_at FROM events WHERE id = $1`, id).
		Scan(&event.ID, &event.Type, &event.Payload, &event.CreatedAt)
	if err != nil {
		return events.Event{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id, endpoint_id, endpoint_url, endpoint_version,
		status, attempt_count, next_attempt_at, terminal_reason, created_at
		FROM deliveries WHERE event_id = $1 ORDER BY endpoint_id`, id)
	if err != nil {
		return events.Event{}, fmt.Errorf("query deliveries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var delivery events.Delivery
		var nextAttempt sql.NullTime
		var terminalReason sql.NullString
		if err := rows.Scan(&delivery.ID, &delivery.EndpointID, &delivery.EndpointURL, &delivery.EndpointVersion,
			&delivery.Status, &delivery.AttemptCount, &nextAttempt, &terminalReason, &delivery.CreatedAt); err != nil {
			return events.Event{}, fmt.Errorf("scan delivery: %w", err)
		}
		if nextAttempt.Valid {
			delivery.NextAttemptAt = &nextAttempt.Time
		}
		if terminalReason.Valid {
			delivery.TerminalReason = terminalReason.String
		}
		event.Deliveries = append(event.Deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return events.Event{}, fmt.Errorf("read deliveries: %w", err)
	}
	sort.Slice(event.Deliveries, func(i, j int) bool { return event.Deliveries[i].EndpointID < event.Deliveries[j].EndpointID })
	return event, nil
}
