//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type darwinRunner struct{}

func platformNativeRunner() Runner {
	return darwinRunner{}
}

func (darwinRunner) Name() string { return "native-darwin" }

func (darwinRunner) Available() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

func (darwinRunner) Exec(ctx context.Context, policy Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, fmt.Errorf("sandbox: empty argv")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return -1, err
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return -1, err
	}
	prof, err := os.CreateTemp("", "codient-sandbox-*.sb")
	if err != nil {
		return -1, err
	}
	profPath := prof.Name()
	defer os.Remove(profPath)
	if _, err := prof.WriteString(darwinSandboxProfile(absWork, policy)); err != nil {
		prof.Close()
		return -1, err
	}
	if err := prof.Close(); err != nil {
		return -1, err
	}

	args := []string{"-f", profPath, "--"}
	args = append(args, argv...)

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "sandbox-exec", args...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
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

// darwinSandboxProfile generates a Seatbelt profile. Network is denied unless AllowNetwork is set.
func darwinSandboxProfile(workspace string, policy Policy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "(version 1)\n")
	fmt.Fprintf(&b, "(deny default)\n")
	fmt.Fprintf(&b, "(allow process-exec process-fork signal)\n")
	fmt.Fprintf(&b, "(allow file-read* file-write* file-ioctl file-read-metadata file-write-metadata file-map-executable\n")
	fmt.Fprintf(&b, "       (subpath %q))\n", workspace)
	for _, p := range policy.ReadOnlyPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ap, err := filepath.Abs(p); err == nil {
			fmt.Fprintf(&b, "(allow file-read* file-read-metadata (subpath %q))\n", ap)
		}
	}
	for _, sys := range []string{"/usr", "/bin", "/sbin", "/lib", "/System", "/private/var/db/dyld", "/dev", "/etc"} {
		fmt.Fprintf(&b, "(allow file-read* file-read-metadata file-map-executable (subpath %q))\n", sys)
	}
	if policy.AllowNetwork {
		fmt.Fprintf(&b, "(allow network-outbound)\n")
		fmt.Fprintf(&b, "(allow network-inbound)\n")
	} else {
		fmt.Fprintf(&b, "(deny network*)\n")
	}
	return b.String()
}
