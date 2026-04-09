package tools

import (
	"context"
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter for network operations.
// Nil receivers are safe and act as no-ops, allowing optional rate limiting.
type RateLimiter struct {
	mu         sync.Mutex
	tokens     float64
	capacity   float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// NewRateLimiter creates a rate limiter that allows ratePerSec requests per second
// with an initial burst capacity of burst requests.
func NewRateLimiter(ratePerSec, burst int) *RateLimiter {
	if ratePerSec <= 0 || burst <= 0 {
		return nil
	}
	return &RateLimiter{
		tokens:     float64(burst),
		capacity:   float64(burst),
		refillRate: float64(ratePerSec),
		lastRefill: time.Now(),
	}
}

// NewNetworkLimiter builds a limiter for fetch_url and web_search from config integers.
// ratePerSec <= 0 disables limiting (returns nil). If burst < 1, burst defaults to ratePerSec.
func NewNetworkLimiter(ratePerSec, burst int) *RateLimiter {
	if ratePerSec <= 0 {
		return nil
	}
	if burst < 1 {
		burst = ratePerSec
	}
	return NewRateLimiter(ratePerSec, burst)
}

// Wait blocks until a token is available or ctx is cancelled.
// Returns ctx.Err() if the context is cancelled before a token is available.
// If rl is nil, Wait returns immediately (no-op).
func (rl *RateLimiter) Wait(ctx context.Context) error {
	if rl == nil {
		return nil
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	for {
		// Refill tokens based on elapsed time
		now := time.Now()
		elapsed := now.Sub(rl.lastRefill).Seconds()
		rl.tokens += elapsed * rl.refillRate
		if rl.tokens > rl.capacity {
			rl.tokens = rl.capacity
		}
		rl.lastRefill = now

		// If we have a token, consume it and return
		if rl.tokens >= 1.0 {
			rl.tokens -= 1.0
			return nil
		}

		// Calculate how long we need to wait for the next token
		tokensNeeded := 1.0 - rl.tokens
		waitDuration := time.Duration(tokensNeeded/rl.refillRate*1e9) * time.Nanosecond

		// Wait for either the duration or context cancellation
		rl.mu.Unlock()
		select {
		case <-time.After(waitDuration):
			rl.mu.Lock()
			// Loop back to check tokens again
		case <-ctx.Done():
			rl.mu.Lock()
			return ctx.Err()
		}
	}
}
