//go:build !windows

package sysutil

// KeepAwake is a no-op outside Windows.
func KeepAwake(bool) {}
