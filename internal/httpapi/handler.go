package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"hookrelay/internal/endpoints"
	"hookrelay/internal/events"
	"hookrelay/internal/logsafe"
	"hookrelay/internal/postgres"
	"hookrelay/internal/ratelimit"
	"hookrelay/internal/targetpolicy"
	"hookrelay/internal/telemetry"
)

const maxRequestBytes = 256 * 1024
const localProducerID = "local-demo-producer"

var eventTypePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type EventStore interface {
	Create(ctx context.Context, producerID, key string, input events.Input) (events.Event, bool, error)
	Get(ctx context.Context, id string) (events.Event, error)
	GetDelivery(ctx context.Context, id string) (events.Delivery, error)
	ListAttempts(ctx context.Context, deliveryID string, limit, offset int) (events.AttemptPage, error)
}

type EndpointStore interface {
	Create(ctx context.Context, name, url, keyID string) (endpoints.Endpoint, error)
	Get(ctx context.Context, id string) (endpoints.Endpoint, error)
	List(ctx context.Context, limit, offset int) (endpoints.Page, error)
	Update(ctx context.Context, id string, change endpoints.Update) (endpoints.Endpoint, error)
}

type MetricsStore interface {
	Metrics(context.Context) (telemetry.Snapshot, error)
}

type ReadinessProbe interface {
	Ready(context.Context) error
}

type handler struct {
	store        EventStore
	endpoints    EndpointStore
	metrics      MetricsStore
	readiness    ReadinessProbe
	token        string
	adminToken   string
	profile      targetpolicy.Profile
	producerRate *ratelimit.Limiter
	adminRate    *ratelimit.Limiter
	metricsRate  *ratelimit.Limiter
}

func New(store EventStore, producerToken string) http.Handler {
	return NewWithAdmin(store, nil, producerToken, "", targetpolicy.ProfilePublic)
}

func NewWithAdmin(store EventStore, endpointStore EndpointStore, producerToken, adminToken string, profile targetpolicy.Profile, metricsStore ...MetricsStore) http.Handler {
	return newWithAdmin(store, endpointStore, producerToken, adminToken, profile, nil, metricsStore...)
}

func NewWithAdminAndHealth(store EventStore, endpointStore EndpointStore, producerToken, adminToken string,
	profile targetpolicy.Profile, metricsStore MetricsStore, readiness ReadinessProbe) http.Handler {
	return newWithAdmin(store, endpointStore, producerToken, adminToken, profile, readiness, metricsStore)
}

func newWithAdmin(store EventStore, endpointStore EndpointStore, producerToken, adminToken string,
	profile targetpolicy.Profile, readiness ReadinessProbe, metricsStore ...MetricsStore) http.Handler {
	h := handler{store: store, endpoints: endpointStore, token: producerToken, adminToken: adminToken,
		profile: profile, readiness: readiness,
		producerRate: ratelimit.New(20, 20), adminRate: ratelimit.New(5, 5), metricsRate: ratelimit.New(1, 2)}
	if len(metricsStore) > 0 {
		h.metrics = metricsStore[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", h.getReady)
	mux.Handle("POST /v1/events", h.authorize(limitRequests(http.HandlerFunc(h.create), h.producerRate, "producer")))
	mux.Handle("GET /v1/events/{id}", h.authorize(http.HandlerFunc(h.get)))
	mux.Handle("GET /v1/deliveries/{id}", h.authorize(http.HandlerFunc(h.getDelivery)))
	mux.Handle("GET /v1/deliveries/{id}/attempts", h.authorize(http.HandlerFunc(h.getAttempts)))
	if endpointStore != nil && adminToken != "" && adminToken != producerToken {
		mux.Handle("POST /v1/endpoints", h.authorizeAdmin(limitRequests(http.HandlerFunc(h.createEndpoint), h.adminRate, "admin")))
		mux.Handle("GET /v1/endpoints", h.authorizeAdmin(http.HandlerFunc(h.listEndpoints)))
		mux.Handle("GET /v1/endpoints/{id}", h.authorizeAdmin(http.HandlerFunc(h.getEndpoint)))
		mux.Handle("PATCH /v1/endpoints/{id}", h.authorizeAdmin(limitRequests(http.HandlerFunc(h.updateEndpoint), h.adminRate, "admin")))
		if h.metrics != nil {
			mux.Handle("GET /v1/metrics", h.authorizeAdmin(limitRequests(http.HandlerFunc(h.getMetrics), h.metricsRate, "metrics")))
		}
	}
	return mux
}

func (h handler) getReady(w http.ResponseWriter, r *http.Request) {
	if h.readiness == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	if err := h.readiness.Ready(ctx); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func limitRequests(next http.Handler, limiter *ratelimit.Limiter, key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowed, _ := limiter.Allow(key); !allowed {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h handler) authorize(next http.Handler) http.Handler {
	return authorizeToken(next, h.token)
}

func (h handler) authorizeAdmin(next http.Handler) http.Handler {
	return authorizeToken(next, h.adminToken)
}

func authorizeToken(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h handler) create(w http.ResponseWriter, r *http.Request) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !idempotencyKeyPattern.MatchString(keys[0]) {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be one 1–128 character ASCII token")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input events.Input
	if err := decoder.Decode(&input); err != nil {
		writeDecodeError(w, err)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "request must contain one JSON value")
		} else {
			writeDecodeError(w, err)
		}
		return
	}
	if err := validateInput(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	event, replayed, err := h.store.Create(r.Context(), localProducerID, keys[0], input)
	if errors.Is(err, postgres.ErrUnknownEndpoint) {
		writeError(w, http.StatusBadRequest, "invalid_endpoint", "endpoint does not exist or is disabled")
		return
	}
	if errors.Is(err, postgres.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was used with different event content")
		return
	}
	if err != nil {
		slog.Error("create event failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "event could not be stored")
		return
	}
	slog.Info("event accepted", "event_id", event.ID, "delivery_count", len(event.Deliveries), "replayed", replayed)
	w.Header().Set("Location", "/v1/events/"+event.ID)
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, event)
}

func (h handler) getMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	snapshot, err := h.metrics.Metrics(ctx)
	if err != nil {
		slog.Error("read metrics failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "metrics could not be read")
		return
	}
	var body bytes.Buffer
	if err := telemetry.WritePrometheus(&body, snapshot); err != nil {
		slog.Error("format metrics failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "metrics_unavailable", "metrics could not be formatted")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(body.Bytes())
}

func (h handler) get(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "event ID must be a UUID")
		return
	}
	event, err := h.store.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "event not found")
		return
	}
	if err != nil {
		slog.Error("read event failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "event could not be read")
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (h handler) getDelivery(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "delivery ID must be a UUID")
		return
	}
	item, err := h.store.GetDelivery(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "delivery not found")
		return
	}
	if err != nil {
		slog.Error("read delivery failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "delivery could not be read")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h handler) getAttempts(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "delivery ID must be a UUID")
		return
	}
	limit, err := intParam(r, "limit", 20, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "limit must be between 1 and 100")
		return
	}
	offset, err := intParam(r, "offset", 0, 0, 10000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "offset must be between 0 and 10000")
		return
	}
	if _, err := h.store.GetDelivery(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "delivery not found")
		return
	} else if err != nil {
		slog.Error("read delivery failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "delivery could not be read")
		return
	}
	page, err := h.store.ListAttempts(r.Context(), id, limit, offset)
	if err != nil {
		slog.Error("read delivery attempts failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "attempts could not be read")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func intParam(r *http.Request, key string, defaultValue, minimum, maximum int) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New("invalid pagination value")
	}
	return parsed, nil
}

func validateInput(input *events.Input) error {
	if !eventTypePattern.MatchString(input.Type) {
		return errors.New("type must be 1–128 ASCII letters, digits, dots, underscores or hyphens and start with a letter")
	}
	var payload map[string]json.RawMessage
	if len(input.Payload) == 0 || json.Unmarshal(input.Payload, &payload) != nil || payload == nil {
		return errors.New("payload must be a JSON object")
	}
	if len(input.EndpointIDs) == 0 || len(input.EndpointIDs) > 10 {
		return errors.New("endpoint_ids must contain 1–10 IDs")
	}
	seen := make(map[string]struct{}, len(input.EndpointIDs))
	for i, id := range input.EndpointIDs {
		id = strings.ToLower(id)
		if !uuidPattern.MatchString(id) {
			return errors.New("each endpoint ID must be a UUID")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("endpoint_ids must be unique")
		}
		seen[id] = struct{}{}
		input.EndpointIDs[i] = id
	}
	sort.Strings(input.EndpointIDs)
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var limitErr *http.MaxBytesError
	if errors.As(err, &limitErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 256 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object with known fields")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
