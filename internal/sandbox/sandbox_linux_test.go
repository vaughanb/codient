//go:build linux

package sandbox

import "testing"

func TestLinuxLandlockSupported_DoesNotPanic(t *testing.T) {
	_ = LinuxLandlockSupported()
}
