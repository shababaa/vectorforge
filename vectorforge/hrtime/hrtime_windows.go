//go:build windows

package hrtime

import (
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	qpc      = kernel32.NewProc("QueryPerformanceCounter")
	freq     int64
)

func init() {
	kernel32.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&freq)))
	if freq <= 0 {
		panic("hrtime: QueryPerformanceFrequency failed")
	}
}

func nanos() int64 {
	var ticks int64
	qpc.Call(uintptr(unsafe.Pointer(&ticks)))
	// Split the conversion so ticks*1e9 cannot overflow.
	return ticks/freq*1e9 + ticks%freq*1e9/freq
}
