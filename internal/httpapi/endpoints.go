package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"hookrelay/internal/endpoints"
	"hookrelay/internal/logsafe"
	"hookrelay/internal/postgres"
	"hookrelay/internal/targetpolicy"
)

const maxEndpointRequestBytes = 2048

var endpointNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type createEndpointInput struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	KeyID string `json:"key_id"`
}

func decodeEndpointRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEndpointRequestBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeEndpointDecodeError(w, err)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			writeEndpointDecodeError(w, err)
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", "request must contain one JSON object")
		}
		return false
	}
	return true
}

func writeEndpointDecodeError(w http.ResponseWriter, err error) {
	var limitErr *http.MaxBytesError
	if errors.As(err, &limitErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 2 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object with known fields")
}

func (h handler) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var input createEndpointInput
	if !decodeEndpointRequest(w, r, &input) {
		return
	}
	if !endpointNamePattern.MatchString(input.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "name must be 1–64 ASCII letters, digits, underscores or hyphens and start with a letter")
		return
	}
	target, err := targetpolicy.ParseForProfile(input.URL, h.profile)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_url", "URL is not allowed by the target profile")
		return
	}
	if !target.Demo && input.KeyID == "" {
		writeError(w, http.StatusBadRequest, "invalid_key_id", "external targets require a configured key_id")
		return
	}
	item, err := h.endpoints.Create(r.Context(), input.Name, input.URL, input.KeyID)
	if errors.Is(err, postgres.ErrEndpointNameConflict) {
		writeError(w, http.StatusConflict, "name_conflict", "endpoint name already exists")
		return
	}
	if errors.Is(err, postgres.ErrInvalidEndpointURL) || errors.Is(err, postgres.ErrUnknownKeyID) {
		writeError(w, http.StatusBadRequest, "invalid_target", "URL or key ID is not configured for this target")
		return
	}
	if err != nil {
		slog.Error("create endpoint failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "endpoint could not be created")
		return
	}
	w.Header().Set("Location", "/v1/endpoints/"+item.ID)
	writeJSON(w, http.StatusCreated, item)
}

func (h handler) listEndpoints(w http.ResponseWriter, r *http.Request) {
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
	page, err := h.endpoints.List(r.Context(), limit, offset)
	if err != nil {
		slog.Error("list endpoints failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "endpoints could not be read")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h handler) getEndpoint(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "endpoint ID must be a UUID")
		return
	}
	item, err := h.endpoints.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	if err != nil {
		slog.Error("read endpoint failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "endpoint could not be read")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h handler) updateEndpoint(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "endpoint ID must be a UUID")
		return
	}
	var change endpoints.Update
	if !decodeEndpointRequest(w, r, &change) {
		return
	}
	if change.ExpectedVersion < 1 || change.URL == nil && change.KeyID == nil && change.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "expected_version and url, key_id or enabled are required")
		return
	}
	if change.URL != nil {
		if _, err := targetpolicy.ParseForProfile(*change.URL, h.profile); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_url", "URL is not allowed by the target profile")
			return
		}
	}
	item, err := h.endpoints.Update(r.Context(), id, change)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	if errors.Is(err, postgres.ErrEndpointVersionConflict) {
		writeError(w, http.StatusConflict, "version_conflict", "endpoint changed; read its current version and retry")
		return
	}
	if errors.Is(err, postgres.ErrInvalidEndpointURL) || errors.Is(err, postgres.ErrUnknownKeyID) {
		writeError(w, http.StatusBadRequest, "invalid_target", "URL or key ID is not configured for this target")
		return
	}
	if err != nil {
		slog.Error("update endpoint failed", "error_kind", logsafe.Kind(err))
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "endpoint could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, item)
}
