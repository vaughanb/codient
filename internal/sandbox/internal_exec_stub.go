//go:build !linux

package sandbox

// RunInternalSandboxExec is a no-op stub; the real implementation is Linux-only.
func RunInternalSandboxExec(_ string) int {
	return 2
}
