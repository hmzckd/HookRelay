package postgres

import (
	"testing"
	"time"

	"hookrelay/internal/delivery"
	"hookrelay/internal/events"
)

func TestMetricsTrackQueueFailureRetryAndSendDuration(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	store := EventStore{DB: db, Jitter: func(time.Duration) time.Duration { return 0 }}
	first, _, err := store.Create(ctx, "test-producer", "metrics-first", events.Input{
		Type: "order.created", Payload: []byte(`{"private":"never-a-label"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET created_at = clock_timestamp() - interval '10 seconds' WHERE id = $1`, first.Deliveries[0].ID); err != nil {
		t.Fatal(err)
	}
	initial, err := store.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.EventsStored != 1 || initial.Deliveries[0] != 1 || initial.QueueReady != 1 || initial.QueueOldestAgeSeconds < 9 {
		t.Fatalf("unexpected pending snapshot: %+v", initial)
	}
	job, err := store.Claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim first: job=%v err=%v", job, err)
	}
	bad := 400
	if err := store.Complete(ctx, *job, delivery.Result{Status: "failed", HTTPStatus: &bad, FailureClass: "http_400"}); err != nil {
		t.Fatal(err)
	}
	failedDelivery, err := store.GetDelivery(ctx, first.Deliveries[0].ID)
	if err != nil || failedDelivery.Status != "dead" || failedDelivery.TerminalReason != "permanent_failure" {
		t.Fatalf("delivery cause: item=%+v err=%v", failedDelivery, err)
	}
	failedAttempts, err := store.ListAttempts(ctx, first.Deliveries[0].ID, 10, 0)
	if err != nil || len(failedAttempts.Items) != 1 || failedAttempts.Items[0].FailureClass != "http_400" {
		t.Fatalf("attempt cause: page=%+v err=%v", failedAttempts, err)
	}
	second, _, err := store.Create(ctx, "test-producer", "metrics-second", events.Input{
		Type: "order.created", Payload: []byte(`{"private":"another"}`),
		EndpointIDs: []string{"11111111-1111-4111-8111-111111111111"},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.Claim(ctx)
	if err != nil || job == nil || job.ID != second.Deliveries[0].ID {
		t.Fatalf("claim second: job=%v err=%v", job, err)
	}
	serverError := 500
	if err := store.Complete(ctx, *job, delivery.Result{Status: "failed", HTTPStatus: &serverError, FailureClass: "http_500"}); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Deliveries[2] != 1 || waiting.Deliveries[5] != 1 || waiting.Attempts[2] != 2 || waiting.AttemptDurationCount != 2 {
		t.Fatalf("unexpected failed/retry snapshot: %+v", waiting)
	}
	job, err = store.Claim(ctx)
	if err != nil || job == nil || job.ID != second.Deliveries[0].ID {
		t.Fatalf("retry claim: job=%v err=%v", job, err)
	}
	ok := 204
	if err := store.Complete(ctx, *job, delivery.Result{Status: "succeeded", HTTPStatus: &ok}); err != nil {
		t.Fatal(err)
	}
	final, err := store.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.EventsStored != 2 || final.Deliveries[3] != 1 || final.Deliveries[5] != 1 || final.QueueReady != 0 ||
		final.QueueOldestAgeSeconds != 0 || final.Attempts[1] != 1 || final.Attempts[2] != 2 ||
		final.RetriesStarted != 1 || final.AttemptDurationCount != 3 || final.AttemptDurationSumSeconds < 0 {
		t.Fatalf("unexpected final snapshot: %+v", final)
	}
}
