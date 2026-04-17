package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// InternalSandboxExecFlag is argv[1] for the Linux sandbox re-exec child (see internal_exec_linux.go).
const InternalSandboxExecFlag = "--internal-sandbox-exec"

// IsInternalSandboxExecChild reports whether argv requests the Linux internal sandbox child.
func IsInternalSandboxExecChild(argv []string) bool {
	return len(argv) >= 3 && argv[1] == InternalSandboxExecFlag
}

// Policy describes filesystem, network, and resource limits for a sandboxed command.
type Policy struct {
	ReadWritePaths []string
	ReadOnlyPaths  []string
	AllowNetwork   bool
	AllowedHosts   []string
	MaxMemoryMB    int
	MaxCPUPercent  int
	MaxProcesses   int
	EnvPassthrough []string
}

// Runner executes argv with optional OS-level isolation.
type Runner interface {
	Exec(ctx context.Context, policy Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (exitCode int, err error)
	Available() bool
	Name() string
}

// NoopRunner runs subprocesses without extra isolation (environment should already be scrubbed by the caller).
type NoopRunner struct{}

func (NoopRunner) Name() string { return "off" }

func (NoopRunner) Available() bool { return true }

func (NoopRunner) Exec(ctx context.Context, policy Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, fmt.Errorf("sandbox: empty argv")
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return -1, fmt.Errorf("command timed out after %v", timeout)
		}
		if errors.Is(runCtx.Err(), context.Canceled) {
			return -1, runCtx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// errRunner always fails Exec with Err.
type errRunner struct {
	name string
	Err  error
}

func (e errRunner) Name() string { return e.name }

func (e errRunner) Available() bool { return false }

func (e errRunner) Exec(context.Context, Policy, string, []string, []string, time.Duration, io.Writer, io.Writer) (int, error) {
	return -1, e.Err
}

// SelectOptions configures SelectRunner.
type SelectOptions struct {
	// ContainerImage is the OCI image for container mode (non-empty enables a default when mode is container).
	ContainerImage string
	// Warn is called when auto mode falls back to a weaker runner (optional).
	Warn func(string)
}

// SelectRunner picks a Runner for the given mode: off, native, container, auto (case-insensitive).
func SelectRunner(mode string, opt SelectOptions) Runner {
	m := strings.TrimSpace(strings.ToLower(mode))
	if m == "" || m == "off" || m == "none" {
		return NoopRunner{}
	}
	if m == "native" {
		r := platformNativeRunner()
		if r.Available() {
			return r
		}
		return errRunner{name: "native", Err: errors.New("native sandbox is not available on this system")}
	}
	if m == "container" {
		r := NewContainerRunner(opt.ContainerImage)
		if r.Available() {
			return r
		}
		return errRunner{name: "container", Err: errors.New("container sandbox: docker or podman not found in PATH")}
	}
	if m == "auto" {
		n := platformNativeRunner()
		if n.Available() {
			return n
		}
		c := NewContainerRunner(opt.ContainerImage)
		if c.Available() {
			if opt.Warn != nil {
				opt.Warn("sandbox auto: using container runtime (native sandbox unavailable)")
			}
			return c
		}
		if opt.Warn != nil {
			opt.Warn("sandbox auto: no native or container sandbox available; subprocesses run without OS-level isolation (environment is still scrubbed)")
		}
		return NoopRunner{}
	}
	return errRunner{name: "invalid", Err: fmt.Errorf("invalid sandbox_mode %q (use off, native, container, auto)", mode)}
}

// ModeIsValid reports whether s is a recognized sandbox mode keyword.
func ModeIsValid(s string) bool {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "", "off", "none", "native", "container", "auto":
		return true
	default:
		return false
	}
}
