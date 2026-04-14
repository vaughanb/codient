package codientcli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"

	"codient/internal/planstore"
	"codient/internal/tools"
)

// VerificationResult captures the outcome of a single verification command.
type VerificationResult struct {
	Label    string
	Command  string
	ExitCode int
	Output   string
	Duration time.Duration
	Passed   bool
}

// runVerification executes the post-execution verification phase: build, tests,
// and lint. Returns true if all checks pass, false otherwise.
func (s *session) runVerification(ctx context.Context, sc *bufio.Scanner, plan *planstore.Plan) (bool, error) {
	plan.Phase = planstore.PhaseReview
	s.planPhase = planstore.PhaseReview
	if err := planstore.Save(plan); err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
	}

	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return true, nil
	}

	var results []VerificationResult

	if cmd := effectiveAutoCheckCmd(s.cfg); cmd != "" {
		r := runVerificationCmd(ctx, ws, "build", cmd, s.cfg.ExecMaxOutputBytes, s.progressOut)
		results = append(results, r)
	}

	if cmd := detectTestCmd(ws); cmd != "" {
		r := runVerificationCmd(ctx, ws, "test", cmd, s.cfg.ExecMaxOutputBytes, s.progressOut)
		results = append(results, r)
	}

	printVerificationSummary(results)

	allPassed := true
	for _, r := range results {
		if !r.Passed {
			allPassed = false
			break
		}
	}

	if allPassed {
		plan.Phase = planstore.PhaseDone
		s.planPhase = planstore.PhaseDone
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "codient: verification passed — plan complete\n")
		return true, nil
	}

	fmt.Fprintf(os.Stderr, "\ncodient: verification failed\n")
	fmt.Fprintf(os.Stderr, "  [f] Re-enter build mode to fix\n")
	fmt.Fprintf(os.Stderr, "  [a] Accept as-is (mark done)\n")
	fmt.Fprintf(os.Stderr, "  [p] Re-plan\n")
	fmt.Fprintf(os.Stderr, "\ncodient: choose action: ")

	if sc == nil || !sc.Scan() {
		return false, nil
	}
	choice := strings.ToLower(strings.TrimSpace(sc.Text()))

	switch {
	case choice == "f" || choice == "fix":
		plan.Phase = planstore.PhaseExecuting
		s.planPhase = planstore.PhaseExecuting
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		failureMsg := buildVerificationFailureMessage(results)
		s.history = append(s.history, openai.UserMessage(failureMsg))
		return false, nil

	case choice == "a" || choice == "accept":
		plan.Phase = planstore.PhaseDone
		s.planPhase = planstore.PhaseDone
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "codient: accepted with failures — plan marked done\n")
		return true, nil

	case choice == "p" || choice == "replan":
		plan.Phase = planstore.PhaseDraft
		s.planPhase = planstore.PhaseDraft
		planstore.IncrementRevision(plan)
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		return false, nil

	default:
		return false, nil
	}
}

func runVerificationCmd(ctx context.Context, workspace, label, cmdLine string, maxOut int, progress io.Writer) VerificationResult {
	if progress != nil {
		fmt.Fprintf(progress, "verification: running %s (%s)...\n", label, cmdLine)
	}

	t0 := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

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
	passed := true
	if err != nil {
		passed = false
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return VerificationResult{
				Label:    label,
				Command:  cmdLine,
				ExitCode: -1,
				Output:   fmt.Sprintf("spawn error: %v", err),
				Duration: dur,
				Passed:   false,
			}
		}
	}

	body := string(out)
	if maxOut > 0 && len(body) > maxOut {
		body = body[:maxOut] + "\n[truncated]\n"
	}

	return VerificationResult{
		Label:    label,
		Command:  cmdLine,
		ExitCode: exitCode,
		Output:   body,
		Duration: dur,
		Passed:   passed,
	}
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

func hasNPMScript(workspace, name string) bool {
	b, err := os.ReadFile(filepath.Join(workspace, "package.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `"`+name+`"`)
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func printVerificationSummary(results []VerificationResult) {
	fmt.Fprintf(os.Stderr, "\ncodient: verification results:\n")
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(os.Stderr, "  %s  %s (%s, %v)\n", status, r.Label, r.Command, r.Duration.Round(time.Millisecond))
	}
}

func buildVerificationFailureMessage(results []VerificationResult) string {
	var b strings.Builder
	b.WriteString("[Verification failed. Fix these issues:]\n\n")
	for _, r := range results {
		if r.Passed {
			continue
		}
		fmt.Fprintf(&b, "## %s (exit %d)\n\n```\n%s\n```\n\n", r.Label, r.ExitCode, strings.TrimSpace(r.Output))
	}
	return b.String()
}
