package codientcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codient/internal/agent"
	"codient/internal/config"
	"codient/internal/tools"
)

const defaultAutoCheckTimeoutSec = 60

// effectiveAutoCheckCmd returns the command to run after file edits, or "" when disabled.
func effectiveAutoCheckCmd(cfg *config.Config) string {
	c := strings.TrimSpace(cfg.AutoCheckCmd)
	if strings.EqualFold(c, "off") {
		return ""
	}
	if c != "" {
		return c
	}
	return detectAutoCheckCmd(cfg.EffectiveWorkspace())
}

// autoCheckTimeoutSec caps exec timeout at defaultAutoCheckTimeoutSec for auto-check runs.
func autoCheckTimeoutSec(cfg *config.Config) int {
	sec := cfg.ExecTimeoutSeconds
	if sec > defaultAutoCheckTimeoutSec {
		sec = defaultAutoCheckTimeoutSec
	}
	if sec < 1 {
		sec = defaultAutoCheckTimeoutSec
	}
	return sec
}

// detectAutoCheckCmd returns a shell command line to validate the project, or "" if unknown.
func detectAutoCheckCmd(workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ""
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return ""
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		return "go build ./..."
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		return "cargo check"
	}
	if fileExists(filepath.Join(root, "package.json")) {
		if fileExists(filepath.Join(root, "tsconfig.json")) {
			return "npx tsc --noEmit"
		}
		if hasNPMBuildScript(root) {
			return "npm run build"
		}
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "setup.cfg")) {
		return "python -m compileall -q ."
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func hasNPMBuildScript(workspace string) bool {
	b, err := os.ReadFile(filepath.Join(workspace, "package.json"))
	if err != nil {
		return false
	}
	var m struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	if m.Scripts == nil {
		return false
	}
	_, ok := m.Scripts["build"]
	return ok
}

// makeAutoCheck runs cmdLine via the platform shell with Dir=workspace.
// On success, Inject is empty and Progress summarizes ok. On failure, Inject contains the full user message.
func makeAutoCheck(workspace, cmdLine string, timeout time.Duration, maxOut int, progress io.Writer) func(context.Context) agent.AutoCheckOutcome {
	cmdLine = strings.TrimSpace(cmdLine)
	return func(ctx context.Context) agent.AutoCheckOutcome {
		if cmdLine == "" {
			return agent.AutoCheckOutcome{}
		}
		t0 := time.Now()
		runCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		var argv []string
		if runtime.GOOS == "windows" {
			argv = []string{"cmd", "/c", cmdLine}
		} else {
			argv = []string{"sh", "-c", cmdLine}
		}
		cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
		cmd.Dir = workspace

		var out []byte
		var err error
		if progress != nil {
			ls := tools.NewLineStreamer(progress)
			cmd.Stdout = ls
			cmd.Stderr = ls
			err = cmd.Run()
			ls.Flush()
			out = ls.Bytes()
		} else {
			out, err = cmd.CombinedOutput()
		}

		dur := time.Since(t0)
		exitCode := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			} else {
				inject := fmt.Sprintf("[auto-check] Build/lint check failed to run.\n\nerror: %v", err)
				prog := fmt.Sprintf("auto-check: %s · %v (spawn error)", cmdLine, dur.Round(time.Millisecond))
				return agent.AutoCheckOutcome{Inject: inject, Progress: prog}
			}
		}
		body := string(out)
		if maxOut > 0 && len(body) > maxOut {
			body = body[:maxOut] + "\n\n[truncated output]\n"
		}
		prog := fmt.Sprintf("auto-check: %s · %v exit=%d", cmdLine, dur.Round(time.Millisecond), exitCode)
		if exitCode == 0 {
			return agent.AutoCheckOutcome{Progress: prog}
		}
		inject := fmt.Sprintf("[auto-check] Build/lint errors after file changes:\n\nexit_code: %d\ncmd: %s\ncwd: %s\n\n%s",
			exitCode, cmdLine, workspace, body)
		return agent.AutoCheckOutcome{Inject: inject, Progress: prog}
	}
}
