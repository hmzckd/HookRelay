package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"hookrelay/internal/logsafe"
)

var ErrClaimLost = errors.New("delivery claim is no longer owned by this worker")

var safeFailureClass = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

const (
	defaultConcurrency   = 4
	maxConcurrency       = 16
	defaultShutdownGrace = 10 * time.Second
)

type Job struct {
	ID          string
	AttemptID   string
	ClaimToken  string
	EventID     string
	EventType   string
	Payload     []byte
	EndpointID  string
	EndpointURL string
	SecretRef   string
}

type Result struct {
	Status       string
	HTTPStatus   *int
	FailureClass string
	RetryAfter   string
}

type Store interface {
	Claim(context.Context) (*Job, error)
	Complete(context.Context, Job, Result) error
}

type Sender interface {
	Send(context.Context, Job) Result
}

type Service struct {
	Store         Store
	Sender        Sender
	PollInterval  time.Duration
	Concurrency   int
	ShutdownGrace time.Duration
	Progress      *Progress
}

func (s Service) ProcessOne(ctx context.Context) (bool, error) {
	job, err := s.Store.Claim(ctx)
	if err != nil || job == nil {
		return false, err
	}
	return true, s.processClaimed(ctx, *job)
}

func (s Service) processClaimed(ctx context.Context, job Job) error {
	slog.Info("delivery attempt started", "event_id", job.EventID, "delivery_id", job.ID,
		"attempt_id", job.AttemptID, "endpoint_id", job.EndpointID)
	started := time.Now()
	result := s.Sender.Send(ctx, job)
	class := result.FailureClass
	if class != "" && !safeFailureClass.MatchString(class) {
		class = "other"
	}
	status := result.Status
	if status != "succeeded" && status != "failed" {
		status = "invalid"
	}
	slog.Info("delivery send finished", "event_id", job.EventID, "delivery_id", job.ID,
		"attempt_id", job.AttemptID, "endpoint_id", job.EndpointID, "result_status", status,
		"failure_class", class, "http_status", result.HTTPStatus, "send_duration_seconds", time.Since(started).Seconds())
	// Forced shutdown leaves an uncertain attempt for lease recovery instead
	// of recording a cancelled HTTP call as a permanent delivery failure.
	if ctx.Err() != nil {
		slog.Warn("delivery completion deferred after shutdown", "event_id", job.EventID,
			"delivery_id", job.ID, "attempt_id", job.AttemptID)
		return nil
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.Complete(finishCtx, job, result); err != nil {
		if errors.Is(err, ErrClaimLost) {
			slog.Warn("delivery claim lost before completion", "event_id", job.EventID,
				"delivery_id", job.ID, "attempt_id", job.AttemptID)
			return nil
		}
		slog.Error("delivery completion failed", "event_id", job.EventID,
			"delivery_id", job.ID, "attempt_id", job.AttemptID)
		return err
	}
	return nil
}

func (s Service) Run(ctx context.Context) error {
	concurrency := s.Concurrency
	if concurrency == 0 {
		concurrency = defaultConcurrency
	}
	if concurrency < 1 || concurrency > maxConcurrency {
		return fmt.Errorf("worker concurrency must be between 1 and %d", maxConcurrency)
	}
	interval := s.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	grace := s.ShutdownGrace
	if grace <= 0 {
		grace = defaultShutdownGrace
	}
	claimCtx, stopClaims := context.WithCancel(ctx)
	defer stopClaims()
	workCtx, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			backoff := 500 * time.Millisecond
			for {
				if claimCtx.Err() != nil {
					return
				}
				job, err := s.Store.Claim(claimCtx)
				if err != nil {
					if claimCtx.Err() != nil {
						return
					}
					if s.Progress != nil {
						s.Progress.storageFailed()
					}
					slog.Warn("worker claim failed; retrying", "error_kind", logsafe.Kind(err), "retry_delay_seconds", backoff.Seconds())
					if !waitFor(claimCtx, backoff) {
						return
					}
					backoff = min(backoff*2, 5*time.Second)
					continue
				}
				if s.Progress != nil {
					s.Progress.pollSucceeded()
				}
				backoff = 500 * time.Millisecond
				if job != nil {
					if s.Progress != nil {
						s.Progress.attemptStarted()
					}
					err := s.processClaimed(workCtx, *job)
					if s.Progress != nil {
						s.Progress.attemptFinished()
					}
					if err != nil {
						if claimCtx.Err() != nil {
							return
						}
						if s.Progress != nil {
							s.Progress.storageFailed()
						}
						slog.Warn("worker completion failed; retrying", "error_kind", logsafe.Kind(err), "retry_delay_seconds", backoff.Seconds())
						if !waitFor(claimCtx, backoff) {
							return
						}
						backoff = min(backoff*2, 5*time.Second)
					}
					continue
				}
				if !waitFor(claimCtx, interval) {
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	<-ctx.Done()
	if s.Progress != nil {
		s.Progress.stop()
	}
	stopClaims()
	select {
	case <-done:
	case <-time.After(grace):
		stopWork()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			return errors.New("worker shutdown exceeded grace period")
		}
	}
	return nil
}

func waitFor(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
