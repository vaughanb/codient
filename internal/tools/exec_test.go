package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCmdKey(t *testing.T) {
	if got := normalizeCmdKey("go"); got != "go" {
		t.Fatalf("go: %q", got)
	}
	if runtime.GOOS == "windows" {
		if got := normalizeCmdKey("GIT.exe"); got != "git" {
			t.Fatalf("git: %q", got)
		}
	} else {
		if got := normalizeCmdKey("git"); got != "git" {
			t.Fatalf("git: %q", got)
		}
	}
	nested := filepath.Join(t.TempDir(), "bin", "go.exe")
	if got := normalizeCmdKey(nested); got != "go" {
		t.Fatalf("path base: %q", got)
	}
}

func TestRunCommandRejectsPathInArgv0(t *testing.T) {
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	_, err := runCommand(context.Background(), dir, ".", []string{filepath.Join("usr", "bin", "go"), "version"}, allow, time.Minute, 1024)
	if err == nil || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunCommandNotAllowlisted(t *testing.T) {
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	_, err := runCommand(context.Background(), dir, ".", []string{"curl", "-V"}, allow, time.Minute, 1024)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "allowlist") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunCommandGoVersion(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	out, err := runCommand(context.Background(), dir, ".", []string{"go", "version"}, allow, 60*time.Second, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("want success: %s", out)
	}
	if !strings.Contains(strings.ToLower(out), "go") {
		t.Fatalf("unexpected: %s", out)
	}
}

func TestRunCommandTruncatesOutput(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	out, err := runCommand(context.Background(), dir, ".", []string{"go", "version"}, allow, 60*time.Second, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[truncated") {
		t.Fatalf("expected truncation: %q", out)
	}
}

func TestRunCommandParentCancel(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	_, err := runCommand(ctx, dir, ".", []string{"go", "version"}, allow, time.Minute, 1024)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunCommandToolViaRegistry(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	dir := t.TempDir()
	r := Default(dir, &ExecOptions{
		Allowlist:      []string{"go"},
		TimeoutSeconds: 60,
		MaxOutputBytes: 32 * 1024,
	})
	out, err := r.Run(context.Background(), "run_command", json.RawMessage(`{"argv":["go","env","GOROOT"],"cwd":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit_code:") {
		t.Fatalf("got %q", out)
	}
}
