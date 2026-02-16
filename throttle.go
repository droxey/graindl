package main

import (
	"context"
	"crypto/rand"
	"math/big"
	"time"
)

// Throttle provides random-duration sleeps in [Min, Max) via crypto/rand.
// Two instances exist by design: Exporter.throttle (between meetings) and
// Scraper.throttle (between API calls). Both are constructed from the same
// config values but operate independently.
type Throttle struct {
	Min time.Duration
	Max time.Duration
}

// Wait sleeps for a random duration in [Min, Max). Returns immediately
// with ctx.Err() if the context is cancelled during the sleep.
func (t *Throttle) Wait(ctx context.Context) error {
	d := t.duration()
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// duration calculates a random sleep time in [Min, Max).
func (t *Throttle) duration() time.Duration {
	if t.Min >= t.Max {
		return t.Min
	}
	spread := t.Max - t.Min
	n, err := rand.Int(rand.Reader, big.NewInt(int64(spread)))
	if err != nil {
		return t.Min + spread/2
	}
	return t.Min + time.Duration(n.Int64())
}
