package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// The numbers both tiers run on, from design §12.
const (
	// DefaultMaxAttempts counts the first call, so it allows two retries.
	DefaultMaxAttempts = 3

	DefaultBase       = 500 * time.Millisecond
	DefaultBackoffCap = 8 * time.Second

	// DefaultMaxDelay is the ceiling on a wait the server itself asked for. Past
	// it the transport tier returns an error instead of sleeping: a provider
	// naming a ten-minute delay should reach the user as a failure rather than
	// as a turn that hangs.
	DefaultMaxDelay = 60 * time.Second
)

// jitter is the share of a computed delay drawn off at random. Subtracted
// rather than added so a jittered delay never exceeds the cap it was clamped to.
const jitter = 0.25

// Sleeper waits for d, or until ctx ends, whichever comes first. It returns
// ctx.Err() only when the context ended first, so a caller can tell an
// interrupted wait from one that finished.
type Sleeper func(ctx context.Context, d time.Duration) error

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Backoff is the exponential delay both tiers wait out between attempts.
type Backoff struct {
	// Base is the delay before the first retry; DefaultBase when zero.
	Base time.Duration

	// Cap is the ceiling the doubling stops at; DefaultBackoffCap when zero.
	Cap time.Duration

	// Rand draws the jitter, uniform in [0,1); rand.Float64 when nil.
	Rand func() float64
}

// Delay is how long to wait before retry n, counting from one.
func (b Backoff) Delay(n int) time.Duration {
	base, ceiling := b.Base, b.Cap
	if base <= 0 {
		base = DefaultBase
	}
	if ceiling <= 0 {
		ceiling = DefaultBackoffCap
	}

	d := base
	// d > 0 is the overflow guard: a Cap high enough that doubling wraps would
	// otherwise leave this loop spinning on a negative or zero delay.
	for i := 1; i < n && d > 0 && d < ceiling; i++ {
		d *= 2
	}
	if d <= 0 || d > ceiling {
		d = ceiling
	}

	draw := b.Rand
	if draw == nil {
		draw = rand.Float64
	}
	return d - time.Duration(float64(d)*jitter*draw())
}
