package delivery

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	MaxAttempts = 5
	MaxAge      = 15 * time.Minute
	BaseDelay   = time.Second
	MaxDelay    = time.Minute
)

type RetryDecision struct {
	Status        string
	NextAttemptAt time.Time
	Reason        string
}

// RetryPolicy separates the decision from real time and randomness so its
// boundaries can be checked deterministically before persistence is added.
type RetryPolicy struct {
	Now    func() time.Time
	Jitter func(max time.Duration) time.Duration
}

func (p RetryPolicy) Decide(result Result, attempts int, createdAt time.Time) RetryDecision {
	if result.Status == "succeeded" {
		return RetryDecision{Status: "succeeded"}
	}
	if result.Status != "failed" || attempts < 1 || createdAt.IsZero() {
		return RetryDecision{Status: "dead", Reason: "invalid_result"}
	}
	if !retryable(result) {
		return RetryDecision{Status: "dead", Reason: "permanent_failure"}
	}
	if attempts >= MaxAttempts {
		return RetryDecision{Status: "dead", Reason: "max_attempts"}
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	if now.Before(createdAt) {
		now = createdAt
	}
	deadline := createdAt.Add(MaxAge)
	if !now.Before(deadline) {
		return RetryDecision{Status: "dead", Reason: "age_exhausted"}
	}
	cap := BaseDelay
	for i := 1; i < attempts && cap < MaxDelay; i++ {
		cap *= 2
		if cap > MaxDelay {
			cap = MaxDelay
		}
	}
	var jitter time.Duration
	if p.Jitter != nil {
		jitter = p.Jitter(cap)
	} else {
		jitter = time.Duration(rand.Int64N(int64(cap) + 1))
	}
	if jitter < 0 || jitter > cap {
		return RetryDecision{Status: "dead", Reason: "invalid_jitter"}
	}
	next := now.Add(jitter)
	if after, ok := parseRetryAfter(result.RetryAfter, now); ok && after.After(next) {
		next = after
	}
	if !next.Before(deadline) {
		return RetryDecision{Status: "dead", Reason: "retry_beyond_age"}
	}
	return RetryDecision{Status: "retry_wait", NextAttemptAt: next, Reason: "transient_failure"}
}

func retryable(result Result) bool {
	switch result.FailureClass {
	case "timeout", "dns_error", "transport_error", "response_read_error":
		return true
	case "target_not_allowed", "secret_unavailable", "invalid_request", "tls_auth_error", "cancelled", "response_too_large":
		return false
	}
	if result.HTTPStatus == nil {
		return false
	}
	status := *result.HTTPStatus
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 && status <= 599
}

func parseRetryAfter(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseUint(raw, 10, 64); err == nil {
		// Any larger delay is beyond the entire retry age budget. Clamp before
		// converting to a duration to avoid overflow on untrusted headers.
		if seconds > uint64(MaxAge/time.Second) {
			return now.Add(MaxAge + time.Second), true
		}
		return now.Add(time.Duration(seconds) * time.Second), true
	}
	if strings.Trim(raw, "0123456789") == "" {
		return now.Add(MaxAge + time.Second), true
	}
	date, err := http.ParseTime(raw)
	if err != nil {
		return time.Time{}, false
	}
	if date.Before(now) {
		return now, true
	}
	return date, true
}
