package receiver

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"hookrelay/internal/logsafe"
	"hookrelay/internal/signing"
)

var ErrConflictingReplay = errors.New("delivery ID was already applied with different content")

type Effect struct {
	DeliveryID  string
	KeyID       string
	EventID     string
	EventType   string
	PayloadHash [sha256.Size]byte
}

type Store interface {
	Record(context.Context, Effect) (bool, error)
}

type Handler struct {
	Store  Store
	Secret []byte
	KeyID  string
	Keys   map[string][]byte
	Mode   string
	Now    func() time.Time

	verifiedCount atomic.Int32
	droppedFirst  atomic.Bool
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/hook" {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	var limitErr *http.MaxBytesError
	if errors.As(err, &limitErr) {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "body read failed", http.StatusBadRequest)
		return
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	keyID := r.Header.Get("X-HookRelay-Key-Id")
	secret := h.Secret
	allowedKey := keyID == h.KeyID
	if h.Keys != nil {
		secret, allowedKey = h.Keys[keyID]
	}
	eventID := r.Header.Get("X-HookRelay-Event-Id")
	eventType := r.Header.Get("X-HookRelay-Event-Type")
	deliveryID := r.Header.Get("X-HookRelay-Delivery-Id")
	attemptID := r.Header.Get("X-HookRelay-Attempt-Id")
	if !allowedKey || !signing.Verify(secret, now,
		r.Header.Get("X-HookRelay-Timestamp"), keyID, eventID, eventType,
		deliveryID, attemptID, body, r.Header.Get("X-HookRelay-Signature")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	attemptNumber := h.verifiedCount.Add(1)
	if h.Mode == "error" || h.Mode == "flaky" && attemptNumber <= 2 {
		http.Error(w, "demo failure", http.StatusInternalServerError)
		return
	}
	if h.Mode == "slow" {
		select {
		case <-time.After(6 * time.Second):
		case <-r.Context().Done():
			return
		}
	}
	effect := Effect{
		DeliveryID:  deliveryID,
		KeyID:       keyID,
		EventID:     eventID,
		EventType:   eventType,
		PayloadHash: sha256.Sum256(body),
	}
	inserted, err := h.Store.Record(r.Context(), effect)
	if errors.Is(err, ErrConflictingReplay) {
		http.Error(w, "delivery ID conflicts with stored content", http.StatusConflict)
		return
	}
	if err != nil {
		slog.Error("demo receiver storage failed", "error_kind", logsafe.Kind(err))
		http.Error(w, "receiver storage failed", http.StatusServiceUnavailable)
		return
	}
	slog.Info("verified webhook", "delivery_id", deliveryID, "attempt_id", attemptID, "new_effect", inserted)
	if h.Mode == "drop-after-commit" && inserted && h.droppedFirst.CompareAndSwap(false, true) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "response-loss demo unavailable", http.StatusInternalServerError)
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		connection.Close()
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
