package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hookrelay/internal/targetpolicy"
)

type testReadiness struct{ err error }

func (p *testReadiness) Ready(context.Context) error { return p.err }

func TestLivenessStaysUpWhenReadinessFails(t *testing.T) {
	probe := &testReadiness{err: errors.New("postgres://private:password@localhost/db")}
	h := NewWithAdminAndHealth(&testStore{}, &testEndpointStore{}, strings.Repeat("p", 32),
		strings.Repeat("a", 32), targetpolicy.ProfileDemo, nil, probe)
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	if got := request("/livez").Code; got != http.StatusNoContent {
		t.Fatalf("liveness during DB outage = %d", got)
	}
	failed := request("/readyz")
	if failed.Code != http.StatusServiceUnavailable || strings.Contains(failed.Body.String(), "private") {
		t.Fatalf("readiness during DB outage: %d %s", failed.Code, failed.Body.String())
	}
	probe.err = nil
	if got := request("/readyz").Code; got != http.StatusNoContent {
		t.Fatalf("readiness after recovery = %d", got)
	}
}
