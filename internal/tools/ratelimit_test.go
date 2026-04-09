package tools

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiter_BurstCapacity(t *testing.T) {
	// 2 requests/sec with burst of 3 should allow 3 immediate requests
	rl := NewRateLimiter(2, 3)
	if rl == nil {
		t.Fatal("expected non-nil limiter")
	}

	ctx := context.Background()
	start := time.Now()

	// First 3 requests should be immediate (within burst)
	for i := 0; i < 3; i++ {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
	}

	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("burst requests took too long: %v", elapsed)
	}
}

func TestRateLimiter_Throttling(t *testing.T) {
	// 10 requests/sec with burst of 2
	rl := NewRateLimiter(10, 2)
	ctx := context.Background()

	// First 2 are immediate
	for i := 0; i < 2; i++ {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("burst request %d failed: %v", i+1, err)
		}
	}

	// Third request should block until refill
	start := time.Now()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("throttled request failed: %v", err)
	}
	elapsed := time.Since(start)

	// At 10/sec, we need 100ms to refill 1 token
	// Allow some tolerance for scheduler jitter
	if elapsed < 50*time.Millisecond {
		t.Errorf("expected throttling delay, got %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("throttling delay too long: %v", elapsed)
	}
}

func TestRateLimiter_ContextCancellation(t *testing.T) {
	// Very slow rate to ensure we block
	rl := NewRateLimiter(1, 1)
	ctx, cancel := context.WithCancel(context.Background())

	// Consume the initial token
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("initial request failed: %v", err)
	}

	// Cancel context before next token is available
	cancel()

	// This should return context.Canceled immediately
	err := rl.Wait(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRateLimiter_ContextTimeout(t *testing.T) {
	// Very slow rate
	rl := NewRateLimiter(1, 1)

	// Consume initial token
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatalf("initial request failed: %v", err)
	}

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Should timeout before next token is available
	start := time.Now()
	err := rl.Wait(ctx)
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestRateLimiter_NilReceiver(t *testing.T) {
	var rl *RateLimiter = nil
	ctx := context.Background()

	// Nil limiter should be a no-op
	if err := rl.Wait(ctx); err != nil {
		t.Errorf("nil limiter Wait failed: %v", err)
	}
}

func TestRateLimiter_InvalidParameters(t *testing.T) {
	tests := []struct {
		name       string
		ratePerSec int
		burst      int
	}{
		{"zero rate", 0, 5},
		{"negative rate", -1, 5},
		{"zero burst", 5, 0},
		{"negative burst", 5, -1},
		{"both zero", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := NewRateLimiter(tt.ratePerSec, tt.burst)
			if rl != nil {
				t.Errorf("expected nil limiter for invalid params, got %+v", rl)
			}
		})
	}
}

func TestRateLimiter_Refill(t *testing.T) {
	// 10 requests/sec with burst of 1
	rl := NewRateLimiter(10, 1)
	ctx := context.Background()

	// Consume initial token
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	// Wait for refill (100ms for 1 token at 10/sec)
	time.Sleep(120 * time.Millisecond)

	// Should have refilled, so this should be immediate
	start := time.Now()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("refilled request failed: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 20*time.Millisecond {
		t.Errorf("refilled request should be immediate, took %v", elapsed)
	}
}

func TestRateLimiter_CapacityCapping(t *testing.T) {
	// 5 requests/sec with burst of 2
	rl := NewRateLimiter(5, 2)
	ctx := context.Background()

	// Consume both burst tokens
	for i := 0; i < 2; i++ {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("burst request %d failed: %v", i+1, err)
		}
	}

	// Wait longer than needed to refill capacity multiple times
	// At 5/sec, 1 second would refill 5 tokens, but capacity is 2
	time.Sleep(1 * time.Second)

	start := time.Now()
	// Should be able to do 2 immediate requests (capacity), then block
	for i := 0; i < 2; i++ {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("capped burst request %d failed: %v", i+1, err)
		}
	}
	immediate := time.Since(start)

	if immediate > 50*time.Millisecond {
		t.Errorf("capped burst should be immediate, took %v", immediate)
	}

	// Third request should block (capacity prevents more than 2)
	start = time.Now()
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("third request failed: %v", err)
	}
	blocked := time.Since(start)

	if blocked < 100*time.Millisecond {
		t.Errorf("expected blocking after capacity exhausted, only took %v", blocked)
	}
}

func TestNewNetworkLimiter_DisabledAndBurstDefault(t *testing.T) {
	if NewNetworkLimiter(0, 5) != nil {
		t.Fatal("expected nil when rate is 0")
	}
	rl := NewNetworkLimiter(3, 0)
	if rl == nil {
		t.Fatal("expected limiter")
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
}
