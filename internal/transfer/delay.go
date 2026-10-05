package transfer

import (
	"context"
	"time"
)

// Delay pauses between scheduled files, including across archive posts.
// The iterator calls Wait serially; bot text requests use their own limiter.
type Delay struct {
	Duration time.Duration
	started  bool
}

func (d *Delay) Wait(ctx context.Context) error {
	if !d.started {
		d.started = true
		return ctx.Err()
	}
	if d.Duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d.Duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
