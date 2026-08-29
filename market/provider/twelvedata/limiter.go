package twelvedata

import (
	"context"
	"sync"
	"time"
)

type Limiter interface{ Wait(context.Context) error }

type spacedLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newSpacedLimiter(requests int, period time.Duration) *spacedLimiter {
	if requests <= 0 {
		requests = 1
	}
	return &spacedLimiter{interval: period / time.Duration(requests)}
}

func (l *spacedLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if l.next.After(now) {
		wait = l.next.Sub(now)
	}
	base := now
	if l.next.After(base) {
		base = l.next
	}
	l.next = base.Add(l.interval)
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type noWaitLimiter struct{}

func (noWaitLimiter) Wait(context.Context) error { return nil }
