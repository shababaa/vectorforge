// Package hrtime is a high-resolution monotonic clock for latency
// measurement.
//
// On Windows, Go's monotonic clock advances in steps of about 0.5 ms
// (503-526 µs measured with go1.26 on the development machine), so a
// 50 µs vector search reads as either 0 or 0.5 ms. QueryPerformanceCounter
// resolves ~100 ns. On other platforms Go's monotonic clock is already
// nanosecond-resolution and is used directly.
package hrtime

import "time"

// Stamp is an opaque point in time from Now.
type Stamp int64

// Since returns the time elapsed since s.
func Since(s Stamp) time.Duration { return time.Duration(nanos() - int64(s)) }

// Now returns the current high-resolution time.
func Now() Stamp { return Stamp(nanos()) }
