package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"hookrelay/internal/events"
	"hookrelay/internal/postgres"
)

type testStore struct {
	calls    int
	err      error
	replayed bool
}

func (s *testStore) Create(_ context.Context, _, _ string, input events.Input) (events.Event, bool, error) {
	s.calls++
	if s.err != nil {
		return events.Event{}, false, s.err
	}
	return events.Event{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Type: input.Type, Payload: input.Payload, Deliveries: []events.Delivery{}}, s.replayed, nil
}

func (*testStore) Get(context.Context, string) (events.Event, error) {
	return events.Event{}, errors.New("unused")
}

func (*testStore) GetDelivery(context.Context, string) (events.Delivery, error) {
	return events.Delivery{}, errors.New("unused")
}

func (*testStore) ListAttempts(context.Context, string, int, int) (events.AttemptPage, error) {
	return events.AttemptPage{}, errors.New("unused")
}

func TestCreateRejectsUnauthorizedAndInvalidRequestsBeforeStorage(t *testing.T) {
	store := &testStore{}
	handler := New(store, strings.Repeat("x", 32))
	cases := []struct {
		name, body, token string
		want              int
	}{
		{"missing token", `{"type":"order.created","payload":{},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`, "", http.StatusUnauthorized},
		{"missing idempotency key", `{"type":"order.created","payload":{},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`, strings.Repeat("x", 32), http.StatusBadRequest},
		{"invalid idempotency key", `{"type":"order.created","payload":{},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`, strings.Repeat("x", 32), http.StatusBadRequest},
		{"duplicate idempotency key", `{"type":"order.created","payload":{},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`, strings.Repeat("x", 32), http.StatusBadRequest},
		{"duplicate targets", `{"type":"order.created","payload":{},"endpoint_ids":["11111111-1111-4111-8111-111111111111","11111111-1111-4111-8111-111111111111"]}`, strings.Repeat("x", 32), http.StatusBadRequest},
		{"oversized body", `{"type":"order.created","payload":{"data":"` + strings.Repeat("a", maxRequestBytes) + `"},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`, strings.Repeat("x", 32), http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.name != "missing idempotency key" {
				key := "test-key-1"
				if tc.name == "invalid idempotency key" {
					key = "bad key"
				}
				req.Header.Set("Idempotency-Key", key)
				if tc.name == "duplicate idempotency key" {
					req.Header.Add("Idempotency-Key", "another-key")
				}
			}
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times for rejected requests", store.calls)
	}
}

func TestCreateReportsConflictAndReplay(t *testing.T) {
	body := `{"type":"order.created","payload":{"order_id":"demo-1"},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`
	for _, tc := range []struct {
		name     string
		store    *testStore
		status   int
		replayed string
	}{
		{"conflict", &testStore{err: postgres.ErrIdempotencyConflict}, http.StatusConflict, ""},
		{"replay", &testStore{replayed: true}, http.StatusAccepted, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "test-key-1")
			recorder := httptest.NewRecorder()
			New(tc.store, strings.Repeat("x", 32)).ServeHTTP(recorder, req)
			if recorder.Code != tc.status || recorder.Header().Get("Idempotency-Replayed") != tc.replayed {
				t.Fatalf("status=%d replayed=%q body=%s", recorder.Code, recorder.Header().Get("Idempotency-Replayed"), recorder.Body.String())
			}
			if tc.store.calls != 1 {
				t.Fatalf("store calls=%d, want 1", tc.store.calls)
			}
		})
	}
}

func TestCreateDoesNotAcknowledgeStorageFailure(t *testing.T) {
	store := &testStore{err: errors.New("database offline")}
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(`{"type":"order.created","payload":{"order_id":"demo-1"},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-1")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	recorder := httptest.NewRecorder()
	New(store, strings.Repeat("x", 32)).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestProducerAcceptanceRateIsBounded(t *testing.T) {
	store := &testStore{}
	token := strings.Repeat("p", 32)
	h := New(store, token)
	body := `{"type":"order.created","payload":{},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`
	rateLimited := 0
	for i := range 50 {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "rate-test-"+strconv.Itoa(i))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code == http.StatusTooManyRequests {
			rateLimited++
			if res.Header().Get("Retry-After") != "1" {
				t.Fatal("rate limit lacks Retry-After")
			}
		} else if res.Code != http.StatusAccepted {
			t.Fatalf("unexpected status %d: %s", res.Code, res.Body.String())
		}
	}
	if rateLimited == 0 || store.calls >= 50 {
		t.Fatalf("rate limit did not protect storage: rejected=%d storage_calls=%d", rateLimited, store.calls)
	}
}

func TestStorageErrorCannotLeakSecretThroughLogOrResponse(t *testing.T) {
	const marker = "private-signing-key-should-not-appear"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	store := &testStore{err: errors.New(marker)}
	request := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(`{"type":"order.created","payload":{},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("p", 32))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "redaction-test")
	response := httptest.NewRecorder()
	New(store, strings.Repeat("p", 32)).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(logs.String(), marker) || strings.Contains(response.Body.String(), marker) {
		t.Fatalf("secret was exposed or wrong response: status=%d logs=%s body=%s", response.Code, logs.String(), response.Body.String())
	}
}

func TestAcceptedEventLogContainsIDButNotPayload(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	token := strings.Repeat("p", 32)
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(`{"type":"order.created","payload":{"private":"do-not-log"},"endpoint_ids":["11111111-1111-4111-8111-111111111111"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "private-idempotency-key")
	response := httptest.NewRecorder()
	New(&testStore{}, token).ServeHTTP(response, req)
	if response.Code != http.StatusAccepted ||
		!strings.Contains(logs.String(), `"event_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`) ||
		strings.Contains(logs.String(), "do-not-log") || strings.Contains(logs.String(), "private-idempotency-key") {
		t.Fatalf("unsafe or missing acceptance log: status=%d logs=%s", response.Code, logs.String())
	}
}
