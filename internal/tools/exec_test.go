package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codient/internal/sandbox"
)

func TestExecEnv_ScrubsSecrets(t *testing.T) {
	old := osEnviron
	t.Cleanup(func() { osEnviron = old })
	osEnviron = func() []string {
		return []string{"PATH=/bin", "HOME=/h", "GITHUB_TOKEN=secret", "CUSTOM=keep"}
	}
	opt := &ExecOptions{EnvPassthrough: []string{"CUSTOM"}}
	env := execEnv(opt)
	s := strings.Join(env, " ")
	if strings.Contains(s, "GITHUB_TOKEN") {
		t.Fatalf("token should be scrubbed: %s", s)
	}
	if !strings.Contains(s, "CUSTOM=keep") {
		t.Fatalf("passthrough missing: %s", s)
	}
}

func TestExecuteSubprocess_UsesSandboxRunner(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	look, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	var called bool
	r := sandboxRunnerFunc(func(ctx context.Context, pol sandbox.Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (int, error) {
		called = true
		return 0, nil
	})
	opt := &ExecOptions{SandboxRunner: r, WorkspaceRoot: t.TempDir()}
	dir := t.TempDir()
	_, err = executeSubprocess(context.Background(), opt, dir, dir, look, []string{"go", "version"}, time.Second, 64, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected sandbox runner to be used")
	}
}

// sandboxRunnerFunc adapts a function to sandbox.Runner for tests.
type sandboxRunnerFunc func(context.Context, sandbox.Policy, string, []string, []string, time.Duration, io.Writer, io.Writer) (int, error)

func (f sandboxRunnerFunc) Exec(ctx context.Context, pol sandbox.Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (int, error) {
	return f(ctx, pol, workDir, argv, env, timeout, stdout, stderr)
}
func (f sandboxRunnerFunc) Available() bool { return true }
func (f sandboxRunnerFunc) Name() string      { return "test" }

func TestShellArgv(t *testing.T) {
	argv, err := ShellArgv("  mkdir foo  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) < 2 {
		t.Fatalf("argv: %v", argv)
	}
	if runtime.GOOS == "windows" {
		if argv[0] != "cmd" || argv[1] != "/c" {
			t.Fatalf("windows argv: %v", argv)
		}
	} else {
		if argv[0] != "sh" || argv[1] != "-c" {
			t.Fatalf("unix argv: %v", argv)
		}
	}
}

func TestNormalizeCmdKey(t *testing.T) {
	if got := NormalizeCmdKey("go"); got != "go" {
		t.Fatalf("go: %q", got)
	}
	if runtime.GOOS == "windows" {
		if got := NormalizeCmdKey("GIT.exe"); got != "git" {
			t.Fatalf("git: %q", got)
		}
	} else {
		if got := NormalizeCmdKey("git"); got != "git" {
			t.Fatalf("git: %q", got)
		}
	}
	nestedName := "go"
	if runtime.GOOS == "windows" {
		nestedName = "go.exe"
	}
	nested := filepath.Join(t.TempDir(), "bin", nestedName)
	if got := NormalizeCmdKey(nested); got != "go" {
		t.Fatalf("path base: %q", got)
	}
}

func TestRunCommandRejectsPathInArgv0(t *testing.T) {
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	_, err := runCommand(context.Background(), dir, ".", []string{filepath.Join("usr", "bin", "go"), "version"}, allow, nil, time.Minute, 1024, nil)
	if err == nil || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunCommandNotAllowlisted(t *testing.T) {
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	_, err := runCommand(context.Background(), dir, ".", []string{"curl", "-V"}, allow, nil, time.Minute, 1024, nil)
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
	out, err := runCommand(context.Background(), dir, ".", []string{"go", "version"}, allow, nil, 60*time.Second, 64*1024, nil)
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
	out, err := runCommand(context.Background(), dir, ".", []string{"go", "version"}, allow, nil, 60*time.Second, 20, nil)
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
	_, err := runCommand(ctx, dir, ".", []string{"go", "version"}, allow, nil, time.Minute, 1024, nil)
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
	}, nil, nil, "", nil, nil, nil)
	out, err := r.Run(context.Background(), "run_command", json.RawMessage(`{"argv":["go","env","GOROOT"],"cwd":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit_code:") {
		t.Fatalf("got %q", out)
	}
}

func TestEnsureExecAllowed_AllowAll(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	sa := NewSessionExecAllow(nil)
	opt := &ExecOptions{
		Session: sa,
		PromptOnDenied: func(context.Context, string, []string) ExecPromptChoice {
			return ExecPromptAllowAll
		},
	}
	look, err := ensureExecAllowedAndResolve(context.Background(), opt, []string{"go", "version"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if look == "" {
		t.Fatal("expected resolved path")
	}
	if !sa.AllowAll() {
		t.Fatal("expected allow-all")
	}
}

func TestEnsureExecAllowed_AllowSession(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	sa := NewSessionExecAllow([]string{})
	opt := &ExecOptions{
		Session: sa,
		PromptOnDenied: func(context.Context, string, []string) ExecPromptChoice {
			return ExecPromptAllowSession
		},
	}
	look, err := ensureExecAllowedAndResolve(context.Background(), opt, []string{"go", "version"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if look == "" {
		t.Fatal("expected resolved path")
	}
	if !sa.IsAllowed("go") {
		t.Fatal("expected go allowed after session grant")
	}
}

func TestStripDotSlash(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantRel bool
	}{
		{"go", "go", false},
		{"./go", "go", true},
		{".\\go.exe", "go.exe", true},
		{"./sub/bin", "sub/bin", true},
		{"git", "git", false},
		{".", ".", false},
		{"../go", "../go", false},
	}
	for _, tt := range tests {
		got, gotRel := stripDotSlash(tt.input)
		if got != tt.want || gotRel != tt.wantRel {
			t.Errorf("stripDotSlash(%q) = (%q, %v), want (%q, %v)", tt.input, got, gotRel, tt.want, tt.wantRel)
		}
	}
}

func TestRunCommandAcceptsDotSlash(t *testing.T) {
	dir := t.TempDir()
	var name string
	if runtime.GOOS == "windows" {
		name = "hello.bat"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("@echo hello\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		name = "hello.sh"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	key := NormalizeCmdKey(name)
	allow := map[string]struct{}{key: {}}
	out, err := runCommand(context.Background(), dir, ".", []string{"./" + name}, allow, nil, 30*time.Second, 64*1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("expected 'hello' in output, got: %s", out)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("expected success, got: %s", out)
	}
}

func TestRunCommandDotSlashStillRejectsDeepPaths(t *testing.T) {
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	_, err := runCommand(context.Background(), dir, ".", []string{"./usr/bin/go", "version"}, allow, nil, time.Minute, 1024, nil)
	if err == nil || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("expected path separator error, got: %v", err)
	}
}

func TestRunCommandDotSlashNotFound(t *testing.T) {
	dir := t.TempDir()
	allow := map[string]struct{}{"nope": {}}
	_, err := runCommand(context.Background(), dir, ".", []string{"./nope"}, allow, nil, time.Minute, 1024, nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if strings.Contains(err.Error(), "path separators") {
		t.Fatalf("should not be a path separator error: %v", err)
	}
}

func TestLineStreamer_BasicLines(t *testing.T) {
	var prog bytes.Buffer
	ls := NewLineStreamer(&prog)
	ls.Write([]byte("line one\nline two\n"))

	if got := string(ls.Bytes()); got != "line one\nline two\n" {
		t.Fatalf("buf: %q", got)
	}
	want := "  | line one\n  | line two\n"
	if got := prog.String(); got != want {
		t.Fatalf("progress:\ngot  %q\nwant %q", got, want)
	}
}

func TestLineStreamer_PartialLines(t *testing.T) {
	var prog bytes.Buffer
	ls := NewLineStreamer(&prog)

	ls.Write([]byte("hel"))
	ls.Write([]byte("lo\nwor"))
	if prog.String() != "  | hello\n" {
		t.Fatalf("after partial writes: %q", prog.String())
	}

	ls.Write([]byte("ld\n"))
	want := "  | hello\n  | world\n"
	if got := prog.String(); got != want {
		t.Fatalf("progress:\ngot  %q\nwant %q", got, want)
	}

	if got := string(ls.Bytes()); got != "hello\nworld\n" {
		t.Fatalf("buf: %q", got)
	}
}

func TestLineStreamer_Flush(t *testing.T) {
	var prog bytes.Buffer
	ls := NewLineStreamer(&prog)
	ls.Write([]byte("no newline"))
	if prog.String() != "" {
		t.Fatalf("should not emit partial before flush: %q", prog.String())
	}
	ls.Flush()
	if got := prog.String(); got != "  | no newline\n" {
		t.Fatalf("after flush: %q", got)
	}
}

func TestLineStreamer_NilProgress(t *testing.T) {
	ls := NewLineStreamer(nil)
	ls.Write([]byte("hello\nworld\n"))
	ls.Flush()
	if got := string(ls.Bytes()); got != "hello\nworld\n" {
		t.Fatalf("buf: %q", got)
	}
}

func TestExecuteSubprocess_Streaming(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	dir := t.TempDir()
	allow := map[string]struct{}{"go": {}}
	var prog bytes.Buffer
	out, err := runCommand(context.Background(), dir, ".", []string{"go", "version"}, allow, nil, 60*time.Second, 64*1024, &prog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("want success: %s", out)
	}
	if !strings.Contains(out, "go version") {
		t.Fatalf("missing go version in output: %s", out)
	}
	if !strings.Contains(prog.String(), "  | ") {
		t.Fatalf("expected streamed lines in progress, got: %q", prog.String())
	}
	if !strings.Contains(prog.String(), "go version") {
		t.Fatalf("expected go version in progress, got: %q", prog.String())
	}
}
