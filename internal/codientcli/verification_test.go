package codientcli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codient/internal/config"
)

func TestRunVerificationCmd_ScrubsEnv(t *testing.T) {
	const leak = "secret-token-must-not-appear-in-child"
	t.Setenv("GITHUB_TOKEN", leak)
	dir := t.TempDir()
	cfg := &config.Config{SandboxMode: "off"}
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `echo %GITHUB_TOKEN%`
	} else {
		cmd = `printf '%s' "$GITHUB_TOKEN"`
	}
	r := runVerificationCmd(context.Background(), cfg, dir, "t", cmd, 10000, nil)
	if !r.Passed {
		t.Fatalf("expected success, got exit %d output %q", r.ExitCode, r.Output)
	}
	if strings.Contains(r.Output, leak) {
		t.Fatalf("GITHUB_TOKEN leaked into verification subprocess output")
	}
}

func TestDetectTestCmd_Go(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644)
	if cmd := detectTestCmd(dir); cmd != "go test ./..." {
		t.Errorf("detectTestCmd = %q, want %q", cmd, "go test ./...")
	}
}

func TestDetectTestCmd_Cargo(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]"), 0o644)
	if cmd := detectTestCmd(dir); cmd != "cargo test" {
		t.Errorf("detectTestCmd = %q, want %q", cmd, "cargo test")
	}
}

func TestDetectTestCmd_NPM(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"test":"jest"}}`), 0o644)
	if cmd := detectTestCmd(dir); cmd != "npm test" {
		t.Errorf("detectTestCmd = %q, want %q", cmd, "npm test")
	}
}

func TestDetectTestCmd_Python(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]"), 0o644)
	os.MkdirAll(filepath.Join(dir, "tests"), 0o755)
	if cmd := detectTestCmd(dir); cmd != "python -m pytest" {
		t.Errorf("detectTestCmd = %q, want %q", cmd, "python -m pytest")
	}
}

func TestDetectTestCmd_Empty(t *testing.T) {
	dir := t.TempDir()
	if cmd := detectTestCmd(dir); cmd != "" {
		t.Errorf("detectTestCmd = %q, want empty", cmd)
	}
}

func TestDetectTestCmd_EmptyString(t *testing.T) {
	if cmd := detectTestCmd(""); cmd != "" {
		t.Errorf("detectTestCmd(\"\") = %q, want empty", cmd)
	}
}

func TestBuildVerificationFailureMessage(t *testing.T) {
	results := []VerificationResult{
		{Label: "build", ExitCode: 0, Passed: true},
		{Label: "test", ExitCode: 1, Output: "FAIL: TestFoo", Passed: false},
		{Label: "lint", ExitCode: 2, Output: "error: unused var", Passed: false},
	}
	msg := buildVerificationFailureMessage(results)
	if !strings.Contains(msg, "Verification failed") {
		t.Error("missing failure header")
	}
	if strings.Contains(msg, "## build") {
		t.Error("passed check should not appear in failure message")
	}
	if !strings.Contains(msg, "## test (exit 1)") {
		t.Error("missing test failure")
	}
	if !strings.Contains(msg, "FAIL: TestFoo") {
		t.Error("missing test output")
	}
	if !strings.Contains(msg, "## lint (exit 2)") {
		t.Error("missing lint failure")
	}
}

func TestBuildVerificationFailureMessage_AllPass(t *testing.T) {
	results := []VerificationResult{
		{Label: "build", Passed: true},
	}
	msg := buildVerificationFailureMessage(results)
	if !strings.Contains(msg, "Verification failed") {
		t.Error("missing header")
	}
	// No failure sections expected.
	if strings.Contains(msg, "## build") {
		t.Error("passed check should not appear")
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	if !dirExists(dir) {
		t.Error("should be true for existing dir")
	}
	if dirExists(filepath.Join(dir, "nope")) {
		t.Error("should be false for missing dir")
	}
	f := filepath.Join(dir, "file.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	if dirExists(f) {
		t.Error("should be false for a file")
	}
}

func TestHasNPMScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"test":"jest","build":"tsc"}}`), 0o644)
	if !hasNPMScript(dir, "test") {
		t.Error("should find test script")
	}
	if !hasNPMScript(dir, "build") {
		t.Error("should find build script")
	}
	if hasNPMScript(dir, "lint") {
		t.Error("should not find lint script")
	}
}

func TestVerificationResultFields(t *testing.T) {
	r := VerificationResult{
		Label:    "build",
		Command:  "go build ./...",
		ExitCode: 0,
		Output:   "",
		Duration: 2 * time.Second,
		Passed:   true,
	}
	if r.Label != "build" || !r.Passed || r.ExitCode != 0 {
		t.Error("unexpected field values")
	}
}
