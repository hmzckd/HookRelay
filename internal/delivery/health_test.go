package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type recoveringStore struct {
	mu          sync.Mutex
	unavailable bool
	job         *Job
	completed   bool
	claimCalls  int
}

func (s *recoveringStore) Claim(context.Context) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls++
	if s.unavailable {
		return nil, errors.New("database unavailable")
	}
	job := s.job
	s.job = nil
	return job, nil
}

func (s *recoveringStore) Complete(context.Context, Job, Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return errors.New("database unavailable")
	}
	s.completed = true
	return nil
}

func (s *recoveringStore) setUnavailable(value bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailable = value
}

func (s *recoveringStore) state() (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completed, s.claimCalls
}

func TestWorkerRemainsLiveAndRecoversWithoutRestart(t *testing.T) {
	store := &recoveringStore{unavailable: true, job: &Job{ID: "one"}}
	progress := &Progress{}
	service := Service{Store: store, Sender: successfulSender{}, Progress: progress, Concurrency: 1,
		PollInterval: 10 * time.Millisecond, ShutdownGrace: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	h := HealthHandler(progress)
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	deadline := time.Now().Add(2 * time.Second)
	for progress.Snapshot(time.Now()).StorageErrors == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker did not observe storage outage")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if request("/livez").Code != http.StatusNoContent || request("/readyz").Code != http.StatusServiceUnavailable {
		t.Fatal("wrong health state during storage outage")
	}
	time.Sleep(100 * time.Millisecond)
	if _, calls := store.state(); calls > 2 {
		t.Fatalf("worker is hammering unavailable storage: %d calls", calls)
	}
	select {
	case err := <-done:
		t.Fatalf("worker exited during outage: %v", err)
	default:
	}
	store.setUnavailable(false)
	deadline = time.Now().Add(3 * time.Second)
	for {
		completed, _ := store.state()
		if completed && request("/readyz").Code == http.StatusNoContent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not recover and complete the queued job")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status ProgressState
	if err := json.Unmarshal(request("/statusz").Body.Bytes(), &status); err != nil || !status.Ready ||
		status.LastPollAt == nil || status.LastAttemptFinishedAt == nil || status.StorageErrors == 0 {
		t.Fatalf("worker progress status: %+v err=%v", status, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not shut down")
	}
}

func TestStaleWorkerPollIsNotReadyButLive(t *testing.T) {
	progress := &Progress{lastPoll: time.Now().Add(-progressMaxAge - time.Second)}
	h := HealthHandler(progress)
	for path, want := range map[string]int{"/livez": http.StatusNoContent, "/readyz": http.StatusServiceUnavailable} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != want {
			t.Fatalf("%s = %d, want %d", path, response.Code, want)
		}
	}
}

func TestWorkerDrainClearsReadiness(t *testing.T) {
	progress := &Progress{}
	progress.pollSucceeded()
	if !progress.Snapshot(time.Now()).Ready {
		t.Fatal("successful poll did not establish readiness")
	}
	progress.stop()
	if progress.Snapshot(time.Now()).Ready {
		t.Fatal("draining worker remained ready")
	}
}
