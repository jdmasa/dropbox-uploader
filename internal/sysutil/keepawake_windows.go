//go:build windows

package sysutil

import (
	"runtime"

	"golang.org/x/sys/windows"
)

var (
	procSetThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	awakeCh                     = make(chan bool)
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

func init() {
	// The execution state belongs to the calling thread, so set and clear it
	// from one locked thread.
	go func() {
		runtime.LockOSThread()
		for on := range awakeCh {
			flags := uintptr(esContinuous)
			if on {
				flags |= esSystemRequired
			}
			procSetThreadExecutionState.Call(flags)
		}
	}()
}

// KeepAwake stops Windows from going to sleep while on is true (the screen may still turn off).
func KeepAwake(on bool) { awakeCh <- on }
