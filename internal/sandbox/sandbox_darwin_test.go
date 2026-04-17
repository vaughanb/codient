//go:build darwin

package sandbox

import (
	"strings"
	"testing"
)

func TestDarwinSandboxProfile_IncludesWorkspace(t *testing.T) {
	p := darwinSandboxProfile("/tmp/ws", Policy{})
	if !strings.Contains(p, "/tmp/ws") {
		t.Fatalf("profile should mention workspace: %s", p)
	}
}
