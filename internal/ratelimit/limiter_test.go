package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestTokenBucketSeparatesTargetsAndRefills(t *testing.T) {
	clock := time.Unix(0, 0)
	limiter := New(2, 2)
	limiter.now = func() time.Time { return clock }
	for range 2 {
		if allowed, _ := limiter.Allow("endpoint-a"); !allowed {
			t.Fatal("burst capacity was too small")
		}
	}
	if allowed, delay := limiter.Allow("endpoint-a"); allowed || delay != 500*time.Millisecond {
		t.Fatalf("third request: allowed=%t delay=%v", allowed, delay)
	}
	if allowed, _ := limiter.Allow("endpoint-b"); !allowed {
		t.Fatal("separate target shared a bucket")
	}
	clock = clock.Add(500 * time.Millisecond)
	if allowed, _ := limiter.Allow("endpoint-a"); !allowed {
		t.Fatal("bucket did not refill")
	}
}

func TestWaitStopsOnCancellation(t *testing.T) {
	limiter := New(1, 1)
	limiter.Allow("target")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Wait(ctx, "target"); err != context.Canceled {
		t.Fatalf("wait ignored cancellation: %v", err)
	}
}
