package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hookrelay/internal/targetpolicy"
	"hookrelay/internal/telemetry"
)

type testMetricsStore struct {
	calls int
	err   error
}

func (s *testMetricsStore) Metrics(context.Context) (telemetry.Snapshot, error) {
	s.calls++
	return telemetry.Snapshot{EventsStored: 3}, s.err
}

func TestMetricsRequireAdminTokenAndReturnPrometheusText(t *testing.T) {
	producer, admin := strings.Repeat("p", 32), strings.Repeat("a", 32)
	metrics := &testMetricsStore{}
	h := NewWithAdmin(&testStore{}, &testEndpointStore{}, producer, admin, targetpolicy.ProfileDemo, metrics)
	request := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		return response
	}
	for _, token := range []string{"", producer} {
		if response := request(token); response.Code != http.StatusUnauthorized {
			t.Fatalf("non-admin status=%d", response.Code)
		}
	}
	if metrics.calls != 0 {
		t.Fatalf("unauthorized requests accessed storage %d times", metrics.calls)
	}
	response := request(admin)
	if response.Code != http.StatusOK || metrics.calls != 1 ||
		!strings.Contains(response.Header().Get("Content-Type"), "version=0.0.4") ||
		!strings.Contains(response.Body.String(), "hookrelay_events_stored 3\n") {
		t.Fatalf("metrics response: status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestMetricsFailureDoesNotExposeStorageError(t *testing.T) {
	const private = "postgres://private:password@127.0.0.1/database"
	admin := strings.Repeat("a", 32)
	h := NewWithAdmin(&testStore{}, &testEndpointStore{}, strings.Repeat("p", 32), admin,
		targetpolicy.ProfileDemo, &testMetricsStore{err: errors.New(private)})
	r := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+admin)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, r)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), private) {
		t.Fatalf("unsafe metrics failure: %d %s", response.Code, response.Body.String())
	}
}
