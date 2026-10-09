//go:build windows

package sysutil

import "golang.org/x/sys/windows"

// ToastsSupported reports whether Windows 10-style toast notifications work
// (Windows 8/8.1 use an older toast format).
func ToastsSupported() bool {
	return windows.RtlGetVersion().MajorVersion >= 10
}
