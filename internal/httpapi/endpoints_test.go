package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hookrelay/internal/endpoints"
	"hookrelay/internal/targetpolicy"
)

type testEndpointStore struct{ creates int }

func (s *testEndpointStore) Create(_ context.Context, name, url, _ string) (endpoints.Endpoint, error) {
	s.creates++
	return endpoints.Endpoint{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: name, URL: url, Version: 1, Enabled: true}, nil
}
func (*testEndpointStore) Get(context.Context, string) (endpoints.Endpoint, error) {
	return endpoints.Endpoint{}, errors.New("unused")
}
func (*testEndpointStore) List(context.Context, int, int) (endpoints.Page, error) {
	return endpoints.Page{Items: []endpoints.Endpoint{}}, nil
}
func (*testEndpointStore) Update(context.Context, string, endpoints.Update) (endpoints.Endpoint, error) {
	return endpoints.Endpoint{}, errors.New("unused")
}

func TestAdminAndProducerTokensAreIsolatedAndUnsafeURLRejected(t *testing.T) {
	producer := strings.Repeat("p", 32)
	admin := strings.Repeat("a", 32)
	store := &testEndpointStore{}
	h := NewWithAdmin(&testStore{}, store, producer, admin, targetpolicy.ProfileDemo)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, r)
		return recorder
	}
	for _, path := range []string{"/v1/endpoints", "/v1/endpoints/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"} {
		if got := request(http.MethodGet, path, producer, "").Code; got != http.StatusUnauthorized {
			t.Fatalf("producer GET %s: %d", path, got)
		}
	}
	if got := request(http.MethodPatch, "/v1/endpoints/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", producer, `{"expected_version":1,"enabled":false}`).Code; got != http.StatusUnauthorized {
		t.Fatalf("producer PATCH: %d", got)
	}
	if got := request(http.MethodPost, "/v1/endpoints", producer, `{"name":"demo-c","url":"http://127.0.0.1:18080/hook"}`).Code; got != http.StatusUnauthorized {
		t.Fatalf("producer POST: %d", got)
	}
	if got := request(http.MethodGet, "/v1/events/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", admin, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("admin producer route: %d", got)
	}
	if got := request(http.MethodPost, "/v1/endpoints", admin, `{"name":"evil","url":"http://127.0.0.1:5432/"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("unsafe URL: %d", got)
	}
	created := request(http.MethodPost, "/v1/endpoints", admin, `{"name":"demo-c","url":"http://127.0.0.1:18080/hook"}`)
	if created.Code != http.StatusCreated || created.Header().Get("Location") != "/v1/endpoints/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "secret_ref") || store.creates != 1 {
		t.Fatalf("secret exposed or unexpected creates: %s / %d", created.Body.String(), store.creates)
	}
}

func TestPublicTargetRequiresConfiguredKeyIDInAdminRequest(t *testing.T) {
	store := &testEndpointStore{}
	admin := strings.Repeat("a", 32)
	h := NewWithAdmin(&testStore{}, store, strings.Repeat("p", 32), admin, targetpolicy.ProfilePublic)
	request := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/endpoints", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+admin)
		r.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		h.ServeHTTP(result, r)
		return result
	}
	if got := request(`{"name":"external-shop","url":"https://hooks.example.com/hook"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("external URL without key ID: %d", got)
	}
	if got := request(`{"name":"demo","url":"http://127.0.0.1:18080/hook"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("demo URL in public profile: %d", got)
	}
	created := request(`{"name":"external-shop","url":"https://hooks.example.com/hook","key_id":"external-shop-v1"}`)
	if created.Code != http.StatusCreated || store.creates != 1 {
		t.Fatalf("external create: %d %s", created.Code, created.Body.String())
	}
}
