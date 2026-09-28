package ratelimit

import (
	"context"
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a process-local token bucket. Keys must be bounded trusted IDs.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]bucket
	capacity float64
	rate     float64
	now      func() time.Time
}

func New(ratePerSecond float64, burst int) *Limiter {
	return &Limiter{buckets: map[string]bucket{}, capacity: float64(burst), rate: ratePerSecond, now: time.Now}
}

func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	state, exists := l.buckets[key]
	if !exists {
		state = bucket{tokens: l.capacity, last: now}
	}
	if elapsed := now.Sub(state.last).Seconds(); elapsed > 0 {
		state.tokens = min(l.capacity, state.tokens+elapsed*l.rate)
		state.last = now
	}
	if state.tokens >= 1 {
		state.tokens--
		l.buckets[key] = state
		return true, 0
	}
	l.buckets[key] = state
	remaining := (1 - state.tokens) / l.rate
	return false, time.Duration(remaining * float64(time.Second))
}

func (l *Limiter) Wait(ctx context.Context, key string) error {
	for {
		allowed, delay := l.Allow(key)
		if allowed {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
