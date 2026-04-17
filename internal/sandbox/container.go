package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultContainerImage = "alpine:3.20"

// ContainerRunner runs commands in Docker or Podman with a read-only root and a workspace bind mount.
type ContainerRunner struct {
	Image string
}

// NewContainerRunner returns a runner that uses Docker or Podman when available.
func NewContainerRunner(image string) *ContainerRunner {
	return &ContainerRunner{Image: strings.TrimSpace(image)}
}

func (c *ContainerRunner) Name() string { return "container" }

func (c *ContainerRunner) image() string {
	if c.Image != "" {
		return c.Image
	}
	return defaultContainerImage
}

func (c *ContainerRunner) runtimePath() (string, error) {
	if p, err := exec.LookPath("docker"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("podman"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("docker or podman not found in PATH")
}

// Available reports whether a container runtime is installed.
func (c *ContainerRunner) Available() bool {
	_, err := c.runtimePath()
	return err == nil
}

// Exec runs argv inside a disposable container. workDir is mounted at /workspace read-write.
func (c *ContainerRunner) Exec(ctx context.Context, policy Policy, workDir string, argv []string, env []string, timeout time.Duration, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, fmt.Errorf("sandbox: empty argv")
	}
	rt, err := c.runtimePath()
	if err != nil {
		return -1, err
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return -1, err
	}

	envFile, err := os.CreateTemp("", "codient-sandbox-env-*")
	if err != nil {
		return -1, err
	}
	envPath := envFile.Name()
	defer os.Remove(envPath)
	for _, e := range env {
		if _, err := envFile.WriteString(e + "\n"); err != nil {
			envFile.Close()
			return -1, err
		}
	}
	if err := envFile.Close(); err != nil {
		return -1, err
	}

	args := []string{
		"run", "--rm",
		"--network=none",
		"-w", "/workspace",
		"-v", volumeMountArg(absWork),
		"--env-file", envPath,
	}
	if policy.MaxMemoryMB > 0 {
		args = append(args, "--memory="+strconv.Itoa(policy.MaxMemoryMB)+"m")
	}
	if policy.MaxCPUPercent > 0 && policy.MaxCPUPercent <= 100 {
		cpus := float64(policy.MaxCPUPercent) / 100.0
		args = append(args, fmt.Sprintf("--cpus=%g", cpus))
	}
	if policy.MaxProcesses > 0 {
		args = append(args, fmt.Sprintf("--pids-limit=%d", policy.MaxProcesses))
	}
	args = append(args, "--read-only")
	args = append(args, c.image())
	args = append(args, argv...)

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, rt, args...)
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

func volumeMountArg(hostPath string) string {
	// Docker Desktop on Windows accepts forward slashes; use clean path.
	if runtime.GOOS == "windows" {
		return hostPath + ":/workspace:rw"
	}
	return hostPath + ":/workspace:rw"
}

// BuildContainerRunArgs returns the docker/podman argv for tests (no execution).
func BuildContainerRunArgs(rt string, image string, workDir string, policy Policy, argv []string, envFile string) ([]string, error) {
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	if image == "" {
		image = defaultContainerImage
	}
	args := []string{
		rt,
		"run", "--rm",
		"--network=none",
		"-w", "/workspace",
		"-v", volumeMountArg(absWork),
		"--env-file", envFile,
	}
	if policy.MaxMemoryMB > 0 {
		args = append(args, "--memory="+strconv.Itoa(policy.MaxMemoryMB)+"m")
	}
	if policy.MaxCPUPercent > 0 && policy.MaxCPUPercent <= 100 {
		cpus := float64(policy.MaxCPUPercent) / 100.0
		args = append(args, fmt.Sprintf("--cpus=%g", cpus))
	}
	if policy.MaxProcesses > 0 {
		args = append(args, fmt.Sprintf("--pids-limit=%d", policy.MaxProcesses))
	}
	args = append(args, "--read-only", image)
	args = append(args, argv...)
	return args, nil
}
