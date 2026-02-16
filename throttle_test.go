package main

import (
	"context"
	"testing"
	"time"
)

func TestThrottleWaitInBounds(t *testing.T) {
	th := &Throttle{
		Min: 50 * time.Millisecond,
		Max: 150 * time.Millisecond,
	}
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		start := time.Now()
		if err := th.Wait(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		elapsed := time.Since(start)

		if elapsed < 40*time.Millisecond {
			t.Errorf("iteration %d: slept too short (%v), min is 50ms", i, elapsed)
		}
		if elapsed > 200*time.Millisecond {
			t.Errorf("iteration %d: slept too long (%v), max is 150ms", i, elapsed)
		}
	}
}

func TestThrottleMinEqualsMax(t *testing.T) {
	th := &Throttle{Min: 50 * time.Millisecond, Max: 50 * time.Millisecond}

	start := time.Now()
	th.Wait(context.Background())
	elapsed := time.Since(start)

	if elapsed < 40*time.Millisecond || elapsed > 100*time.Millisecond {
		t.Errorf("when min==max, should sleep ~50ms, got %v", elapsed)
	}
}

func TestThrottleMinGreaterThanMax(t *testing.T) {
	th := &Throttle{Min: 100 * time.Millisecond, Max: 50 * time.Millisecond}

	start := time.Now()
	th.Wait(context.Background())
	elapsed := time.Since(start)

	if elapsed < 90*time.Millisecond {
		t.Errorf("when min>max, should sleep min (100ms), got %v", elapsed)
	}
}

func TestThrottleZero(t *testing.T) {
	th := &Throttle{Min: 0, Max: time.Millisecond}

	start := time.Now()
	th.Wait(context.Background())
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("zero-min throttle should be near-instant, got %v", elapsed)
	}
}

func TestThrottleCancelledContext(t *testing.T) {
	th := &Throttle{Min: 5 * time.Second, Max: 10 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after 50ms — should return immediately, not sleep 5s
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := th.Wait(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected context cancellation error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("should have returned quickly on cancel, took %v", elapsed)
	}
}

func TestThrottleAlreadyCancelled(t *testing.T) {
	th := &Throttle{Min: time.Second, Max: 2 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	err := th.Wait(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error for already-cancelled context")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("already-cancelled should be instant, took %v", elapsed)
	}
}
