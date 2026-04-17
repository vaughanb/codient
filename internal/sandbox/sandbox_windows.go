//go:build windows

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsRunner struct{}

func platformNativeRunner() Runner {
	return windowsRunner{}
}

func (windowsRunner) Name() string { return "native-windows" }

func (windowsRunner) Available() bool { return true }

func (windowsRunner) Exec(ctx context.Context, policy Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, fmt.Errorf("sandbox: empty argv")
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return -1, err
	}
	defer windows.CloseHandle(job)

	var jeli windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	jeli.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if policy.MaxMemoryMB > 0 {
		jeli.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		jeli.JobMemoryLimit = uintptr(policy.MaxMemoryMB) * 1024 * 1024
	}
	if policy.MaxProcesses > 0 {
		jeli.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		jeli.BasicLimitInformation.ActiveProcessLimit = uint32(policy.MaxProcesses)
	}
	ret, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&jeli)),
		uint32(unsafe.Sizeof(jeli)),
	)
	if ret == 0 {
		return -1, err
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
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		return -1, err
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = cmd.Process.Kill()
		return -1, err
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return -1, fmt.Errorf("command timed out after %v", timeout)
		}
		if errors.Is(runCtx.Err(), context.Canceled) {
			return -1, runCtx.Err()
		}
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, waitErr
	}
	return 0, nil
}
