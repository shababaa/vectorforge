//go:build !windows

package hrtime

import "time"

var base = time.Now()

func nanos() int64 { return int64(time.Since(base)) }
