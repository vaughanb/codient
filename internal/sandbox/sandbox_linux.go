//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type linuxRunner struct{}

func platformNativeRunner() Runner {
	return linuxRunner{}
}

func (linuxRunner) Name() string { return "native-linux" }

func (linuxRunner) Available() bool {
	return LinuxLandlockSupported()
}

func (linuxRunner) Exec(ctx context.Context, policy Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, fmt.Errorf("sandbox: empty argv")
	}
	exe, err := os.Executable()
	if err != nil {
		return -1, err
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return -1, err
	}

	rw := append([]string{absWork}, policy.ReadWritePaths...)
	var rwClean []string
	seen := map[string]struct{}{}
	for _, d := range rw {
		d = filepath.Clean(d)
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		rwClean = append(rwClean, d)
	}
	ro := append([]string(nil), policy.ReadOnlyPaths...)
	for _, d := range defaultLinuxRODirs() {
		if _, ok := seen[d]; !ok {
			ro = append(ro, d)
		}
	}

	payload := linuxSandboxPayload{
		Argv:    append([]string(nil), argv...),
		WorkDir: absWork,
		Env:     append([]string(nil), env...),
		RWDirs:  rwClean,
		RODirs:  ro,
	}
	body, err := json.Marshal(&payload)
	if err != nil {
		return -1, err
	}
	tmp, err := os.CreateTemp("", "codient-sandbox-*.json")
	if err != nil {
		return -1, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return -1, err
	}
	if err := tmp.Close(); err != nil {
		return -1, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, exe, InternalSandboxExecFlag, tmpPath)
	cmd.Dir = workDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
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
