//go:build !windows

package sysutil

// ToastsSupported reports whether system notifications can be used.
func ToastsSupported() bool { return true }
