package delivery

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type unsafeClassSender struct{}

func (unsafeClassSender) Send(context.Context, Job) Result {
	return Result{Status: "failed", FailureClass: "secret=do-not-log"}
}

func TestAttemptLogsCorrelateWithoutPayloadURLOrSecret(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	store := &queuedStore{jobs: []Job{{
		ID: "delivery-1", EventID: "event-1", AttemptID: "attempt-1", EndpointID: "endpoint-1",
		EndpointURL: "https://private.example/hook", SecretRef: "secret-file-name",
		Payload: []byte(`{"private":"payload-value"}`),
	}}}
	if found, err := (Service{Store: store, Sender: unsafeClassSender{}}).ProcessOne(context.Background()); !found || err != nil {
		t.Fatalf("process: found=%t err=%v", found, err)
	}
	output := logs.String()
	for _, required := range []string{`"event_id":"event-1"`, `"delivery_id":"delivery-1"`,
		`"attempt_id":"attempt-1"`, `"send_duration_seconds":`, `"failure_class":"other"`} {
		if !strings.Contains(output, required) {
			t.Fatalf("missing correlation field %s in %s", required, output)
		}
	}
	for _, forbidden := range []string{"private.example", "secret-file-name", "payload-value", "do-not-log"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("sensitive value %q leaked in %s", forbidden, output)
		}
	}
}

type lostClaimStore struct{ claimed bool }

func (s *lostClaimStore) Claim(context.Context) (*Job, error) {
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return &Job{ID: "old-claim"}, nil
}

func (*lostClaimStore) Complete(context.Context, Job, Result) error { return ErrClaimLost }

type successfulSender struct{}

func (successfulSender) Send(context.Context, Job) Result { return Result{Status: "succeeded"} }

func TestLostClaimDoesNotStopWorker(t *testing.T) {
	service := Service{Store: &lostClaimStore{}, Sender: successfulSender{}}
	if found, err := service.ProcessOne(context.Background()); !found || err != nil {
		t.Fatalf("lost claim stopped worker: found=%t err=%v", found, err)
	}
	if found, err := service.ProcessOne(context.Background()); found || err != nil {
		t.Fatalf("worker did not continue polling: found=%t err=%v", found, err)
	}
}

type queuedStore struct {
	mu        sync.Mutex
	jobs      []Job
	claimed   int
	completed int
}

func (s *queuedStore) Claim(context.Context) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		return nil, nil
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	s.claimed++
	return &job, nil
}

func (s *queuedStore) Complete(context.Context, Job, Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed++
	return nil
}

func (s *queuedStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimed, s.completed
}

type gatedSender struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (s *gatedSender) Send(ctx context.Context, job Job) Result {
	active := s.active.Add(1)
	for {
		peak := s.peak.Load()
		if active <= peak || s.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	s.started <- job.ID
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	s.active.Add(-1)
	return Result{Status: "succeeded"}
}

func TestRunClaimsOnlyFreeCapacityAndDrainsOnShutdown(t *testing.T) {
	store := &queuedStore{}
	for i := range 10 {
		store.jobs = append(store.jobs, Job{ID: fmt.Sprint(i)})
	}
	sender := &gatedSender{started: make(chan string, 10), release: make(chan struct{})}
	service := Service{Store: store, Sender: sender, Concurrency: 3, PollInterval: time.Millisecond, ShutdownGrace: time.Second}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	for range 3 {
		select {
		case <-sender.started:
		case <-time.After(2 * time.Second):
			t.Fatal("initial capacity was not filled")
		}
	}
	if claimed, _ := store.counts(); claimed != 3 || sender.peak.Load() != 3 {
		t.Fatalf("startup capacity: claimed=%d peak=%d", claimed, sender.peak.Load())
	}
	select {
	case id := <-sender.started:
		t.Fatalf("job %s started without free capacity", id)
	case <-time.After(50 * time.Millisecond):
	}
	sender.release <- struct{}{}
	select {
	case <-sender.started:
	case <-time.After(2 * time.Second):
		t.Fatal("free capacity was not refilled")
	}
	stop()
	close(sender.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not drain in-flight jobs")
	}
	claimed, completed := store.counts()
	if claimed != 4 || completed != 4 || sender.peak.Load() > 3 {
		t.Fatalf("drain result: claimed=%d completed=%d peak=%d", claimed, completed, sender.peak.Load())
	}
}

type waitForCancellationSender struct{ started chan struct{} }

func (s waitForCancellationSender) Send(ctx context.Context, _ Job) Result {
	close(s.started)
	<-ctx.Done()
	return Result{Status: "failed", FailureClass: "cancelled"}
}

func TestRunForcedShutdownLeavesUncertainAttemptForLeaseRecovery(t *testing.T) {
	store := &queuedStore{jobs: []Job{{ID: "one"}}}
	sender := waitForCancellationSender{started: make(chan struct{})}
	service := Service{Store: store, Sender: sender, Concurrency: 1, ShutdownGrace: 30 * time.Millisecond}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-sender.started:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start")
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forced shutdown did not finish")
	}
	claimed, completed := store.counts()
	if claimed != 1 || completed != 0 {
		t.Fatalf("cancelled send was recorded as a completed delivery: claimed=%d completed=%d", claimed, completed)
	}
}
