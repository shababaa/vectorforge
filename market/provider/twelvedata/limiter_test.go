package twelvedata

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSpacedLimiterWaitIsCancellable(t *testing.T) {
	limiter := newSpacedLimiter(1, time.Hour)
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
