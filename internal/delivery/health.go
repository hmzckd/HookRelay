package delivery

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const progressMaxAge = 10 * time.Second

// Progress is process-local; its zero value is ready for use.
type Progress struct {
	mu                    sync.Mutex
	lastPoll              time.Time
	lastAttemptFinished   time.Time
	inFlight              int
	storageErrors         uint64
	storageErrorUncleared bool
	stopping              bool
}

type ProgressState struct {
	Ready                 bool       `json:"ready"`
	LastPollAt            *time.Time `json:"last_poll_at,omitempty"`
	LastAttemptFinishedAt *time.Time `json:"last_attempt_finished_at,omitempty"`
	InFlight              int        `json:"in_flight"`
	StorageErrors         uint64     `json:"storage_errors"`
}

func (p *Progress) pollSucceeded() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastPoll = time.Now().UTC()
	p.storageErrorUncleared = false
}

func (p *Progress) storageFailed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.storageErrors++
	p.storageErrorUncleared = true
}

func (p *Progress) attemptStarted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inFlight++
}

func (p *Progress) attemptFinished() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inFlight--
	p.lastAttemptFinished = time.Now().UTC()
}

func (p *Progress) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopping = true
}

func (p *Progress) Snapshot(now time.Time) ProgressState {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := ProgressState{InFlight: p.inFlight, StorageErrors: p.storageErrors}
	if !p.lastPoll.IsZero() {
		poll := p.lastPoll
		state.LastPollAt = &poll
		state.Ready = !p.stopping && !p.storageErrorUncleared && now.Sub(poll) <= progressMaxAge
	}
	if !p.lastAttemptFinished.IsZero() {
		finished := p.lastAttemptFinished
		state.LastAttemptFinishedAt = &finished
	}
	return state
}

func HealthHandler(progress *Progress) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !progress.Snapshot(time.Now()).Ready {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /statusz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(progress.Snapshot(time.Now()))
	})
	return mux
}
