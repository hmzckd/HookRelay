package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"hookrelay/internal/delivery"
	"hookrelay/internal/events"
)

func retentionInput() events.Input {
	return events.Input{Type: "order.created", Payload: []byte(`{"retention":true}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"}}
}

func ageEvent(t *testing.T, ctx context.Context, db *sql.DB, eventID string) {
	t.Helper()
	for _, statement := range []string{
		`UPDATE events SET created_at = clock_timestamp() - interval '31 days' WHERE id = $1`,
		`UPDATE deliveries SET created_at = clock_timestamp() - interval '31 days' WHERE event_id = $1`,
		`UPDATE idempotency_keys SET created_at = clock_timestamp() - interval '31 days' WHERE event_id = $1`,
		`UPDATE delivery_attempts SET started_at = clock_timestamp() - interval '31 days',
			finished_at = clock_timestamp() - interval '31 days' WHERE delivery_id IN
			(SELECT id FROM deliveries WHERE event_id = $1)`,
	} {
		if _, err := db.ExecContext(ctx, statement, eventID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRetentionDeletesOnlyOldTerminalEventAndReleasesIdempotencyKey(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	terminal, _, err := store.Create(ctx, "producer", "old-terminal", retentionInput())
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil || job == nil || job.EventID != terminal.ID {
		t.Fatalf("claim terminal: job=%+v err=%v", job, err)
	}
	status := 204
	if err := store.Complete(ctx, *job, delivery.Result{Status: "succeeded", HTTPStatus: &status}); err != nil {
		t.Fatal(err)
	}
	ageEvent(t, ctx, db, terminal.ID)
	if _, err := db.ExecContext(ctx, `INSERT INTO demo_receiver_effects
		(delivery_id, key_id, event_id, event_type, payload_sha256, applied_at)
		VALUES ($1, 'demo/a-v1', $2, 'order.created', $3, clock_timestamp() - interval '31 days')`,
		terminal.Deliveries[0].ID, terminal.ID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	pending, _, err := store.Create(ctx, "producer", "old-pending", retentionInput())
	if err != nil {
		t.Fatal(err)
	}
	ageEvent(t, ctx, db, pending.ID)
	if count, err := store.CountExpired(ctx); err != nil || count != 1 {
		t.Fatalf("eligible count=%d err=%v", count, err)
	}
	if count, err := store.CleanupExpired(ctx, 1); err != nil || count != 1 {
		t.Fatalf("cleanup count=%d err=%v", count, err)
	}
	if _, err := store.Get(ctx, terminal.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired event remains: %v", err)
	}
	if found, err := store.Get(ctx, pending.ID); err != nil || found.Deliveries[0].Status != "pending" {
		t.Fatalf("unfinished event was removed: %+v err=%v", found, err)
	}
	for table, want := range map[string]int{"idempotency_keys": 1, "delivery_attempts": 0,
		"deliveries": 1, "demo_receiver_effects": 0} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("unexpected %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	newEvent, replayed, err := store.Create(ctx, "producer", "old-terminal", retentionInput())
	if err != nil || replayed || newEvent.ID == terminal.ID {
		t.Fatalf("expired key did not start a new window: id=%s replayed=%t err=%v", newEvent.ID, replayed, err)
	}
}

func TestRetentionWaitsForRecentReceiverEffect(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	event, _, err := store.Create(ctx, "producer", "recent-effect", retentionInput())
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%+v err=%v", job, err)
	}
	status := 204
	if err := store.Complete(ctx, *job, delivery.Result{Status: "succeeded", HTTPStatus: &status}); err != nil {
		t.Fatal(err)
	}
	ageEvent(t, ctx, db, event.ID)
	if _, err := db.ExecContext(ctx, `INSERT INTO demo_receiver_effects
		(delivery_id, key_id, event_id, event_type, payload_sha256)
		VALUES ($1, 'demo/a-v1', $2, 'order.created', $3)`,
		event.Deliveries[0].ID, event.ID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountExpired(ctx); err != nil || count != 0 {
		t.Fatalf("recent receiver effect not protected: count=%d err=%v", count, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE demo_receiver_effects
		SET applied_at = clock_timestamp() - interval '31 days' WHERE delivery_id = $1`, event.Deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountExpired(ctx); err != nil || count != 1 {
		t.Fatalf("old receiver effect blocked cleanup: count=%d err=%v", count, err)
	}
}

func TestRetentionRejectsUnboundedBatch(t *testing.T) {
	store := EventStore{}
	for _, limit := range []int{0, MaxCleanupBatch + 1} {
		if _, err := store.CleanupExpired(context.Background(), limit); err == nil {
			t.Fatalf("unsafe batch %d accepted", limit)
		}
	}
}

func TestRetentionStopsAtBatchLimit(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	status := 204
	for _, key := range []string{"batch-one", "batch-two"} {
		event, _, err := store.Create(ctx, "producer", key, retentionInput())
		if err != nil {
			t.Fatal(err)
		}
		job, err := store.Claim(ctx)
		if err != nil || job == nil || job.EventID != event.ID {
			t.Fatalf("claim %s: job=%+v err=%v", key, job, err)
		}
		if err := store.Complete(ctx, *job, delivery.Result{Status: "succeeded", HTTPStatus: &status}); err != nil {
			t.Fatal(err)
		}
		ageEvent(t, ctx, db, event.ID)
	}
	if count, err := store.CleanupExpired(ctx, 1); err != nil || count != 1 {
		t.Fatalf("first batch count=%d err=%v", count, err)
	}
	if remaining, err := store.CountExpired(ctx); err != nil || remaining != 1 {
		t.Fatalf("first batch remaining=%d err=%v", remaining, err)
	}
	if count, err := store.CleanupExpired(ctx, 1); err != nil || count != 1 {
		t.Fatalf("second batch count=%d err=%v", count, err)
	}
	if remaining, err := store.CountExpired(ctx); err != nil || remaining != 0 {
		t.Fatalf("second batch remaining=%d err=%v", remaining, err)
	}
}

func TestConcurrentExpiredKeyReplayAndCleanupReturnAValidResult(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db}
	old, _, err := store.Create(ctx, "producer", "racing-key", retentionInput())
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%+v err=%v", job, err)
	}
	status := 204
	if err := store.Complete(ctx, *job, delivery.Result{Status: "succeeded", HTTPStatus: &status}); err != nil {
		t.Fatal(err)
	}
	ageEvent(t, ctx, db, old.ID)
	raceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	cleanupDone := make(chan error, 1)
	createDone := make(chan struct {
		id       string
		replayed bool
		err      error
	}, 1)
	go func() {
		<-start
		_, err := store.CleanupExpired(raceCtx, 1)
		cleanupDone <- err
	}()
	go func() {
		<-start
		event, replayed, err := store.Create(raceCtx, "producer", "racing-key", retentionInput())
		createDone <- struct {
			id       string
			replayed bool
			err      error
		}{event.ID, replayed, err}
	}()
	close(start)
	var result struct {
		id       string
		replayed bool
		err      error
	}
	select {
	case result = <-createDone:
	case <-raceCtx.Done():
		t.Fatal("concurrent replay did not complete")
	}
	if result.err != nil || result.id == "" || result.replayed != (result.id == old.ID) {
		t.Fatalf("invalid concurrent replay: %+v old_id=%s", result, old.ID)
	}
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatalf("concurrent cleanup failed: %v", err)
		}
	case <-raceCtx.Done():
		t.Fatal("concurrent cleanup did not complete")
	}
}
