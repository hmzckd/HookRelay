package postgres

import (
	"database/sql"
	"errors"
	"testing"

	"hookrelay/internal/delivery"
	"hookrelay/internal/endpoints"
	"hookrelay/internal/events"
	"hookrelay/internal/targetpolicy"
)

func TestManagedEndpointVersionDisableAndSnapshot(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	managed := EndpointStore{DB: db, Profile: targetpolicy.ProfileDemo}
	urlA := "http://127.0.0.1:18080/hook"
	urlB := "http://127.0.0.1:18081/hook"
	item, err := managed.Create(ctx, "managed-a", urlA, "")
	if err != nil || item.Version != 1 || !item.Enabled {
		t.Fatalf("create: %+v %v", item, err)
	}
	if _, err := managed.Create(ctx, "managed-a", urlA, ""); !errors.Is(err, ErrEndpointNameConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := managed.Create(ctx, "forbidden", "http://127.0.0.1:5432/", ""); !errors.Is(err, ErrInvalidEndpointURL) {
		t.Fatalf("forbidden URL: %v", err)
	}
	page, err := managed.List(ctx, 2, 0)
	if err != nil || len(page.Items) != 2 || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("page: %+v %v", page, err)
	}
	producer := EventStore{DB: db}
	input := events.Input{Type: "order.created", Payload: []byte(`{"order_id":"managed"}`), EndpointIDs: []string{item.ID}}
	first, _, err := producer.Create(ctx, "producer", "managed-first", input)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := managed.Update(ctx, item.ID, endpoints.Update{ExpectedVersion: 1, URL: &urlB})
	if err != nil || updated.Version != 2 || updated.URL != urlB {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := managed.Update(ctx, item.ID, endpoints.Update{ExpectedVersion: 1, URL: &urlA}); !errors.Is(err, ErrEndpointVersionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	second, _, err := producer.Create(ctx, "producer", "managed-second", input)
	if err != nil {
		t.Fatal(err)
	}
	firstJob, err := producer.Claim(ctx)
	if err != nil || firstJob == nil {
		t.Fatalf("claim first: %+v %v", firstJob, err)
	}
	assertSnapshot(t, firstJob, first.Deliveries[0].ID, urlA, "demo/a-v1")
	if err := producer.Complete(ctx, *firstJob, delivery.Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	secondJob, err := producer.Claim(ctx)
	if err != nil || secondJob == nil {
		t.Fatalf("claim second: %+v %v", secondJob, err)
	}
	assertSnapshot(t, secondJob, second.Deliveries[0].ID, urlB, "demo/b-v1")
	if err := producer.Complete(ctx, *secondJob, delivery.Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	third, _, err := producer.Create(ctx, "producer", "managed-third", input)
	if err != nil {
		t.Fatal(err)
	}
	falseValue := false
	updated, err = managed.Update(ctx, item.ID, endpoints.Update{ExpectedVersion: 2, Enabled: &falseValue})
	if err != nil || updated.Enabled || updated.Version != 3 {
		t.Fatalf("disable: %+v %v", updated, err)
	}
	state, err := producer.GetDelivery(ctx, third.Deliveries[0].ID)
	if err != nil || state.Status != "dead" || state.TerminalReason != "endpoint_disabled" {
		t.Fatalf("waiting job after disable: %+v %v", state, err)
	}
	if _, _, err := producer.Create(ctx, "producer", "managed-fourth", input); !errors.Is(err, ErrUnknownEndpoint) {
		t.Fatalf("new event after disable: %v", err)
	}
	if job, err := producer.Claim(ctx); err != nil || job != nil {
		t.Fatalf("disabled target claim: %+v %v", job, err)
	}
	if _, err := managed.Get(ctx, "ffffffff-ffff-4fff-8fff-ffffffffffff"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing endpoint: %v", err)
	}
	trueValue := true
	if _, err := managed.Update(ctx, item.ID, endpoints.Update{ExpectedVersion: 3, Enabled: &trueValue}); err != nil {
		t.Fatal(err)
	}
	state, err = producer.GetDelivery(ctx, third.Deliveries[0].ID)
	if err != nil || state.Status != "dead" {
		t.Fatalf("disabled job was resurrected: %+v %v", state, err)
	}
	fourth, _, err := producer.Create(ctx, "producer", "managed-fifth", input)
	if err != nil {
		t.Fatal(err)
	}
	processing, err := producer.Claim(ctx)
	if err != nil || processing == nil || processing.ID != fourth.Deliveries[0].ID {
		t.Fatalf("claim before disable: %+v %v", processing, err)
	}
	if _, err := managed.Update(ctx, item.ID, endpoints.Update{ExpectedVersion: 4, Enabled: &falseValue}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deliveries SET lease_until = clock_timestamp() - interval '1 second' WHERE id = $1`, processing.ID); err != nil {
		t.Fatal(err)
	}
	if job, err := producer.Claim(ctx); err != nil || job != nil {
		t.Fatalf("disabled abandoned job was resent: %+v %v", job, err)
	}
	state, err = producer.GetDelivery(ctx, processing.ID)
	if err != nil || state.Status != "dead" || state.TerminalReason != "endpoint_disabled" {
		t.Fatalf("abandoned disabled job: %+v %v", state, err)
	}
	attempts, err := producer.ListAttempts(ctx, processing.ID, 10, 0)
	if err != nil || len(attempts.Items) != 1 || attempts.Items[0].Status != "unknown" {
		t.Fatalf("abandoned attempt history: %+v %v", attempts, err)
	}
}

func assertSnapshot(t *testing.T, job *delivery.Job, id, url, secretRef string) {
	t.Helper()
	if job.ID != id || job.EndpointURL != url || job.SecretRef != secretRef {
		t.Fatalf("delivery changed target: %+v", job)
	}
}

func TestExternalKeyRotationKeepsOldDeliverySnapshot(t *testing.T) {
	ctx, db, _ := testDatabase(t)
	managed := EndpointStore{DB: db, Profile: targetpolicy.ProfilePublic, KeyIDs: map[string]struct{}{
		"external-shop-v1": {}, "external-shop-v2": {},
	}}
	url := "https://hooks.example.com/hook"
	if _, err := managed.Create(ctx, "missing-key", url, "external-missing-v1"); !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("unknown key accepted: %v", err)
	}
	if _, err := managed.Create(ctx, "demo-in-public", "http://127.0.0.1:18080/hook", ""); !errors.Is(err, ErrInvalidEndpointURL) {
		t.Fatalf("demo URL accepted in public profile: %v", err)
	}
	item, err := managed.Create(ctx, "external-shop", url, "external-shop-v1")
	if err != nil || item.KeyID != "external-shop-v1" {
		t.Fatalf("create external target: %+v %v", item, err)
	}
	producer := EventStore{DB: db}
	input := events.Input{Type: "order.created", Payload: []byte(`{"order_id":"rotate"}`), EndpointIDs: []string{item.ID}}
	oldEvent, _, err := producer.Create(ctx, "producer", "before-rotation", input)
	if err != nil {
		t.Fatal(err)
	}
	newKeyID := "external-shop-v2"
	updated, err := managed.Update(ctx, item.ID, endpoints.Update{ExpectedVersion: 1, KeyID: &newKeyID})
	if err != nil || updated.Version != 2 || updated.KeyID != newKeyID {
		t.Fatalf("rotate key: %+v %v", updated, err)
	}
	newEvent, _, err := producer.Create(ctx, "producer", "after-rotation", input)
	if err != nil {
		t.Fatal(err)
	}
	oldJob, err := producer.Claim(ctx)
	if err != nil || oldJob == nil {
		t.Fatalf("old claim: %+v %v", oldJob, err)
	}
	assertSnapshot(t, oldJob, oldEvent.Deliveries[0].ID, url, "external-shop-v1")
	if err := producer.Complete(ctx, *oldJob, delivery.Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	newJob, err := producer.Claim(ctx)
	if err != nil || newJob == nil {
		t.Fatalf("new claim: %+v %v", newJob, err)
	}
	assertSnapshot(t, newJob, newEvent.Deliveries[0].ID, url, "external-shop-v2")
}
