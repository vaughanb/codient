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

// effectiveLintCmd returns the lint command for the auto-check sequence, or "" when disabled.
func effectiveLintCmd(cfg *config.Config) string {
	c := strings.TrimSpace(cfg.LintCmd)
	if strings.EqualFold(c, "off") {
		return ""
	}
	if c != "" {
		return c
	}
	return detectLintCmd(cfg.EffectiveWorkspace())
}

// effectiveTestCmd returns the test command for the auto-check sequence, or "" when disabled.
func effectiveTestCmd(cfg *config.Config) string {
	c := strings.TrimSpace(cfg.TestCmd)
	if strings.EqualFold(c, "off") {
		return ""
	}
	if c != "" {
		return c
	}
	return detectTestCmd(cfg.EffectiveWorkspace())
}

// autoCheckStep is one build/lint/test command in the post-edit sequence.
type autoCheckStep struct {
	label   string
	cmdLine string
}

// buildAutoCheckSteps returns ordered build → lint → test steps (skips empty commands).
func buildAutoCheckSteps(cfg *config.Config) []autoCheckStep {
	var out []autoCheckStep
	if b := effectiveAutoCheckCmd(cfg); b != "" {
		out = append(out, autoCheckStep{"build", b})
	}
	if l := effectiveLintCmd(cfg); l != "" {
		out = append(out, autoCheckStep{"lint", l})
	}
	if t := effectiveTestCmd(cfg); t != "" {
		out = append(out, autoCheckStep{"test", t})
	}
	return out
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

// detectLintCmd returns a lint command for the workspace, or "" if unknown / unavailable.
func detectLintCmd(workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ""
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return ""
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		if _, err := exec.LookPath("golangci-lint"); err == nil {
			return "golangci-lint run ./..."
		}
		return ""
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		return "cargo clippy -- -D warnings"
	}
	if fileExists(filepath.Join(root, "package.json")) {
		if hasNPMScript(root, "lint") {
			return "npm run lint"
		}
		return ""
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "setup.cfg")) {
		if _, err := exec.LookPath("ruff"); err == nil {
			return "ruff check ."
		}
		if _, err := exec.LookPath("flake8"); err == nil {
			return "flake8"
		}
		return ""
	}
	return ""
}

// detectTestCmd returns a shell command to run the project's test suite, or "".
func detectTestCmd(workspace string) string {
	root := strings.TrimSpace(workspace)
	if root == "" {
		return ""
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		return "go test ./..."
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		return "cargo test"
	}
	if fileExists(filepath.Join(root, "package.json")) {
		if hasNPMScript(root, "test") {
			return "npm test"
		}
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "setup.cfg")) {
		if fileExists(filepath.Join(root, "pytest.ini")) || dirExists(filepath.Join(root, "tests")) || dirExists(filepath.Join(root, "test")) {
			return "python -m pytest"
		}
	}
	return ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
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

func hasNPMScript(workspace, name string) bool {
	b, err := os.ReadFile(filepath.Join(workspace, "package.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `"`+name+`"`)
}

// makeAutoCheck runs a single build-step command (legacy helper; same as one-step sequence).
func makeAutoCheck(workspace, cmdLine string, timeout time.Duration, maxOut int, progress io.Writer) func(context.Context) agent.AutoCheckOutcome {
	cmdLine = strings.TrimSpace(cmdLine)
	if cmdLine == "" {
		return func(context.Context) agent.AutoCheckOutcome { return agent.AutoCheckOutcome{} }
	}
	return makeAutoCheckSequence(workspace, []autoCheckStep{{"build", cmdLine}}, timeout, maxOut, progress)
}

// makeAutoCheckSequence runs build → lint → test in order (fail-fast). Progress aggregates successful steps.
func makeAutoCheckSequence(workspace string, steps []autoCheckStep, timeout time.Duration, maxOut int, progress io.Writer) func(context.Context) agent.AutoCheckOutcome {
	return func(ctx context.Context) agent.AutoCheckOutcome {
		if len(steps) == 0 {
			return agent.AutoCheckOutcome{}
		}
		var progLines []string
		for _, st := range steps {
			cmdLine := strings.TrimSpace(st.cmdLine)
			if cmdLine == "" {
				continue
			}
			out := execOneAutoCheck(ctx, workspace, st.label, cmdLine, timeout, maxOut, progress)
			if out.Progress != "" {
				progLines = append(progLines, out.Progress)
			}
			if out.Inject != "" {
				combinedProg := strings.Join(progLines, "\n")
				return agent.AutoCheckOutcome{Inject: out.Inject, Progress: combinedProg}
			}
		}
		return agent.AutoCheckOutcome{Progress: strings.Join(progLines, "\n")}
	}
}

func execOneAutoCheck(ctx context.Context, workspace, label, cmdLine string, timeout time.Duration, maxOut int, progress io.Writer) agent.AutoCheckOutcome {
	cmdLine = strings.TrimSpace(cmdLine)
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
	argv, err := tools.ShellArgv(cmdLine)
	if err != nil {
		return agent.AutoCheckOutcome{}
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = workspace

	var out []byte
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
			inject := fmt.Sprintf("[auto-check] %s check failed to run.\n\nerror: %v", label, err)
			prog := fmt.Sprintf("auto-check [%s]: %s · %v (spawn error)", label, cmdLine, dur.Round(time.Millisecond))
			return agent.AutoCheckOutcome{Inject: inject, Progress: prog}
		}
	}
	body := string(out)
	if maxOut > 0 && len(body) > maxOut {
		body = body[:maxOut] + "\n\n[truncated output]\n"
	}
	prog := fmt.Sprintf("auto-check [%s]: %s · %v exit=%d", label, cmdLine, dur.Round(time.Millisecond), exitCode)
	if exitCode == 0 {
		return agent.AutoCheckOutcome{Progress: prog}
	}
	inject := fmt.Sprintf("[auto-check] %s errors after file changes:\n\nexit_code: %d\ncmd: %s\ncwd: %s\n\n%s",
		label, exitCode, cmdLine, workspace, body)
	return agent.AutoCheckOutcome{Inject: inject, Progress: prog}
}
