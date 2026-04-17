//go:build integration

package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Live Docker/Podman tests. Run with:
//
//	CODIENT_INTEGRATION=1 go test -tags=integration -run TestIntegration_Container ./internal/sandbox/...
//
// Skipped when CODIENT_INTEGRATION is not set or no container runtime is available.

func skipUnlessContainerIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("CODIENT_INTEGRATION") != "1" {
		t.Skip("set CODIENT_INTEGRATION=1 to run container integration tests")
	}
	if testing.Short() {
		t.Skip("skipping container integration in -short mode")
	}
	c := NewContainerRunner("")
	if !c.Available() {
		t.Skip("docker or podman not available on PATH")
	}
}

func TestIntegration_ContainerRunner_BasicCommand(t *testing.T) {
	skipUnlessContainerIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var stdout, stderr bytes.Buffer
	c := NewContainerRunner("")
	dir := t.TempDir()
	code, err := c.Exec(ctx, Policy{}, dir, []string{"sh", "-c", "echo hello"}, nil, 60*time.Second, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Exec: %v stderr=%s", err, stderr.String())
	}
	if code != 0 {
		t.Fatalf("exit code %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello") {
		t.Fatalf("stdout=%q want hello", stdout.String())
	}
}

func TestIntegration_ContainerRunner_WorkspaceIsolation(t *testing.T) {
	skipUnlessContainerIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "in_workspace.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "host_only.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewContainerRunner("")
	var out bytes.Buffer

	code, err := c.Exec(ctx, Policy{}, ws, []string{"cat", "in_workspace.txt"}, nil, 60*time.Second, &out, os.Stderr)
	if err != nil || code != 0 {
		t.Fatalf("read workspace file: code=%d err=%v out=%q", code, err, out.String())
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("expected workspace file contents, got %q", out.String())
	}

	out.Reset()
	code, err = c.Exec(ctx, Policy{}, ws, []string{"sh", "-c", "test ! -f ../outside/host_only.txt && test ! -f /outside/host_only.txt"}, nil, 60*time.Second, &out, os.Stderr)
	if err != nil || code != 0 {
		t.Fatalf("host-only file should not be visible in container: code=%d err=%v stderr check failed", code, err)
	}
}
