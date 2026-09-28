package delivery

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func intPointer(value int) *int { return &value }

func TestRetryPolicyClassifiesFailuresAndHonorsRetryAfter(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	policy := RetryPolicy{
		Now:    func() time.Time { return now },
		Jitter: func(max time.Duration) time.Duration { return max / 2 },
	}
	for _, tc := range []struct {
		name       string
		result     Result
		attempts   int
		createdAt  time.Time
		status     string
		reason     string
		nextOffset time.Duration
	}{
		{"success", Result{Status: "succeeded"}, 1, now.Add(-time.Minute), "succeeded", "", 0},
		{"server error", Result{Status: "failed", HTTPStatus: intPointer(500), FailureClass: "http_500"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"request timeout", Result{Status: "failed", HTTPStatus: intPointer(408), FailureClass: "http_408"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"rate limit delta", Result{Status: "failed", HTTPStatus: intPointer(429), FailureClass: "http_429", RetryAfter: "10"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 10 * time.Second},
		{"service unavailable date", Result{Status: "failed", HTTPStatus: intPointer(503), FailureClass: "http_503", RetryAfter: now.Add(12 * time.Second).Format(http.TimeFormat)}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 12 * time.Second},
		{"invalid retry after", Result{Status: "failed", HTTPStatus: intPointer(429), FailureClass: "http_429", RetryAfter: "not-a-date"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"past retry after", Result{Status: "failed", HTTPStatus: intPointer(429), FailureClass: "http_429", RetryAfter: now.Add(-time.Minute).Format(http.TimeFormat)}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"dns", Result{Status: "failed", FailureClass: "dns_error"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"timeout", Result{Status: "failed", FailureClass: "timeout"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"temporary transport", Result{Status: "failed", FailureClass: "transport_error"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"response read", Result{Status: "failed", FailureClass: "response_read_error"}, 1, now.Add(-time.Minute), "retry_wait", "transient_failure", 500 * time.Millisecond},
		{"bad request", Result{Status: "failed", HTTPStatus: intPointer(400), FailureClass: "http_400"}, 1, now.Add(-time.Minute), "dead", "permanent_failure", 0},
		{"redirect", Result{Status: "failed", HTTPStatus: intPointer(302), FailureClass: "http_302"}, 1, now.Add(-time.Minute), "dead", "permanent_failure", 0},
		{"security policy", Result{Status: "failed", FailureClass: "target_not_allowed"}, 1, now.Add(-time.Minute), "dead", "permanent_failure", 0},
		{"tls authentication", Result{Status: "failed", HTTPStatus: intPointer(500), FailureClass: "tls_auth_error"}, 1, now.Add(-time.Minute), "dead", "permanent_failure", 0},
		{"missing secret", Result{Status: "failed", FailureClass: "secret_unavailable"}, 1, now.Add(-time.Minute), "dead", "permanent_failure", 0},
		{"too many attempts", Result{Status: "failed", HTTPStatus: intPointer(500)}, MaxAttempts, now.Add(-time.Minute), "dead", "max_attempts", 0},
		{"age exhausted", Result{Status: "failed", HTTPStatus: intPointer(500)}, 1, now.Add(-MaxAge), "dead", "age_exhausted", 0},
		{"retry after beyond age", Result{Status: "failed", HTTPStatus: intPointer(429), RetryAfter: "120"}, 1, now.Add(-14 * time.Minute), "dead", "retry_beyond_age", 0},
		{"huge numeric retry after", Result{Status: "failed", HTTPStatus: intPointer(429), RetryAfter: strings.Repeat("9", 40)}, 1, now.Add(-time.Minute), "dead", "retry_beyond_age", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.Decide(tc.result, tc.attempts, tc.createdAt)
			if got.Status != tc.status || got.Reason != tc.reason {
				t.Fatalf("decision = %+v, want %s/%s", got, tc.status, tc.reason)
			}
			if tc.status == "retry_wait" && !got.NextAttemptAt.Equal(now.Add(tc.nextOffset)) {
				t.Fatalf("next attempt = %v, want %v", got.NextAttemptAt, now.Add(tc.nextOffset))
			}
			if tc.status != "retry_wait" && !got.NextAttemptAt.IsZero() {
				t.Fatalf("terminal decision has next attempt: %+v", got)
			}
		})
	}
}

func TestRetryPolicyBackoffAndDeadlineBoundary(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	var observed []time.Duration
	policy := RetryPolicy{
		Now: func() time.Time { return now },
		Jitter: func(max time.Duration) time.Duration {
			observed = append(observed, max)
			return max
		},
	}
	result := Result{Status: "failed", HTTPStatus: intPointer(500)}
	for attempts, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		decision := policy.Decide(result, attempts+1, now.Add(-time.Minute))
		if decision.Status != "retry_wait" || !decision.NextAttemptAt.Equal(now.Add(want)) {
			t.Fatalf("attempt %d: %+v", attempts+1, decision)
		}
	}
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if observed[i] != want {
			t.Fatalf("jitter cap %d = %v, want %v", i, observed[i], want)
		}
	}
	// A scheduled attempt at the exact age limit is too late.
	decision := policy.Decide(result, 1, now.Add(-MaxAge+time.Second))
	if decision.Status != "dead" || decision.Reason != "retry_beyond_age" {
		t.Fatalf("deadline boundary: %+v", decision)
	}
}
