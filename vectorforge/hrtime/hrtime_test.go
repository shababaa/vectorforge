package hrtime

import (
	"testing"
	"time"
)

func TestResolutionIsFinerThanTenMicroseconds(t *testing.T) {
	best := time.Hour
	for i := 0; i < 100; i++ {
		a := Now()
		var d time.Duration
		for d == 0 {
			d = Since(a)
		}
		best = min(best, d)
	}
	if best > 10*time.Microsecond {
		t.Fatalf("smallest observable step %v", best)
	}
	t.Logf("smallest observable step: %v", best)
}

func TestAgreesWithWallClock(t *testing.T) {
	a, wall := Now(), time.Now()
	time.Sleep(50 * time.Millisecond)
	got, want := Since(a), time.Since(wall)
	if diff := got - want; diff < -2*time.Millisecond || diff > 2*time.Millisecond {
		t.Fatalf("hrtime %v vs time %v", got, want)
	}
}

func BenchmarkNowSince(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Since(Now())
	}
}
