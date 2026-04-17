package codientcli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"

	"codient/internal/config"
	"codient/internal/planstore"
	"codient/internal/sandbox"
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
		r := runVerificationCmd(ctx, s.cfg, ws, "build", cmd, s.cfg.ExecMaxOutputBytes, s.progressOut)
		results = append(results, r)
	}

	if cmd := effectiveLintCmd(s.cfg); cmd != "" {
		r := runVerificationCmd(ctx, s.cfg, ws, "lint", cmd, s.cfg.ExecMaxOutputBytes, s.progressOut)
		results = append(results, r)
	}

	if cmd := effectiveTestCmd(s.cfg); cmd != "" {
		r := runVerificationCmd(ctx, s.cfg, ws, "test", cmd, s.cfg.ExecMaxOutputBytes, s.progressOut)
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

	switch choice {
	case "f", "fix":
		plan.Phase = planstore.PhaseExecuting
		s.planPhase = planstore.PhaseExecuting
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		failureMsg := buildVerificationFailureMessage(results)
		s.history = append(s.history, openai.UserMessage(failureMsg))
		return false, nil

	case "a", "accept":
		plan.Phase = planstore.PhaseDone
		s.planPhase = planstore.PhaseDone
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "codient: accepted with failures — plan marked done\n")
		return true, nil

	case "p", "replan":
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

const verificationTimeoutSec = 120

func runVerificationCmd(ctx context.Context, cfg *config.Config, workspace, label, cmdLine string, maxOut int, progress io.Writer) VerificationResult {
	if progress != nil {
		fmt.Fprintf(progress, "verification: running %s (%s)...\n", label, cmdLine)
	}

	t0 := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, verificationTimeoutSec*time.Second)
	defer cancel()

	argv, err := tools.ShellArgv(cmdLine)
	if err != nil {
		return VerificationResult{
			Label:    label,
			Command:  cmdLine,
			ExitCode: -1,
			Output:   fmt.Sprintf("invalid command: %v", err),
			Duration: time.Since(t0),
			Passed:   false,
		}
	}
	runner := sandbox.SelectRunner(cfg.SandboxMode, sandbox.SelectOptions{ContainerImage: cfg.SandboxContainerImage})
	wsAbs, err := filepath.Abs(workspace)
	if err != nil {
		wsAbs = workspace
	}
	pol := sandbox.Policy{
		ReadWritePaths: []string{wsAbs},
		ReadOnlyPaths:  append([]string(nil), cfg.SandboxReadOnlyPaths...),
		EnvPassthrough: cfg.ExecEnvPassthrough,
	}
	env := sandbox.ScrubEnv(os.Environ(), cfg.ExecEnvPassthrough)
	timeout := time.Duration(verificationTimeoutSec) * time.Second

	var out []byte
	var exitCode int
	if progress != nil {
		ls := tools.NewLineStreamer(progress)
		exitCode, err = runner.Exec(runCtx, pol, workspace, argv, env, timeout, ls, ls)
		ls.Flush()
		out = ls.Bytes()
	} else {
		var buf bytes.Buffer
		exitCode, err = runner.Exec(runCtx, pol, workspace, argv, env, timeout, &buf, &buf)
		out = buf.Bytes()
	}

	dur := time.Since(t0)
	passed := true
	if err != nil {
		return VerificationResult{
			Label:    label,
			Command:  cmdLine,
			ExitCode: -1,
			Output:   fmt.Sprintf("sandbox exec error: %v", err),
			Duration: dur,
			Passed:   false,
		}
	}
	if exitCode != 0 {
		passed = false
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
