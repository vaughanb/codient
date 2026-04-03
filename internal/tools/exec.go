package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ExecOptions configures the run_command tool. Pass nil to Default to disable it.
type ExecOptions struct {
	Allowlist      []string
	TimeoutSeconds int
	MaxOutputBytes int
}

func allowSet(allow []string) map[string]struct{} {
	m := make(map[string]struct{}, len(allow))
	for _, a := range allow {
		a = strings.TrimSpace(strings.ToLower(a))
		a = strings.TrimSuffix(a, ".exe")
		a = strings.TrimSuffix(a, ".bat")
		a = strings.TrimSuffix(a, ".cmd")
		if a != "" {
			m[a] = struct{}{}
		}
	}
	return m
}

// normalizeCmdKey maps argv[0] to an allowlist key (basename, lower, strip .exe on Windows).
func normalizeCmdKey(argv0 string) string {
	base := filepath.Base(strings.TrimSpace(argv0))
	s := strings.ToLower(base)
	if runtime.GOOS == "windows" {
		s = strings.TrimSuffix(s, ".exe")
		s = strings.TrimSuffix(s, ".bat")
		s = strings.TrimSuffix(s, ".cmd")
	}
	return s
}

func runCommand(ctx context.Context, workspaceRoot, cwdRel string, argv []string, allow map[string]struct{}, timeout time.Duration, maxOut int) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("argv must be non-empty")
	}
	if strings.ContainsAny(argv[0], `/\`) {
		return "", fmt.Errorf("argv[0] must be a command name without path separators (got %q)", argv[0])
	}
	key := normalizeCmdKey(argv[0])
	if key == "" {
		return "", fmt.Errorf("empty command name")
	}
	if _, ok := allow[key]; !ok {
		return "", fmt.Errorf("command %q is not on CODIENT_EXEC_ALLOWLIST", key)
	}

	cwd := strings.TrimSpace(cwdRel)
	if cwd == "" {
		cwd = "."
	}
	workDir, err := absUnderRoot(workspaceRoot, cwd)
	if err != nil {
		return "", err
	}

	look, err := exec.LookPath(argv[0])
	if err != nil {
		return "", fmt.Errorf("look path %q: %w", argv[0], err)
	}
	resolvedKey := normalizeCmdKey(look)
	if _, ok := allow[resolvedKey]; !ok {
		return "", fmt.Errorf("resolved binary %q is not allowlisted", resolvedKey)
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, look, argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = execEnv()

	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("command timed out after %v", timeout)
		}
		if errors.Is(runCtx.Err(), context.Canceled) {
			return "", runCtx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return "", fmt.Errorf("run %v: %w", argv, err)
		}
	}

	trunc := ""
	if maxOut > 0 && len(out) > maxOut {
		out = out[:maxOut]
		trunc = fmt.Sprintf("\n\n[truncated output at %d bytes]", maxOut)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\n", exitCode)
	fmt.Fprintf(&b, "cwd: %s\n", workDir)
	fmt.Fprintf(&b, "argv: %q\n\n", argv)
	b.Write(out)
	b.WriteString(trunc)
	return b.String(), nil
}

func execEnv() []string {
	return append([]string{}, osEnviron()...)
}

// osEnviron exists for tests to stub.
var osEnviron = func() []string { return os.Environ() }
