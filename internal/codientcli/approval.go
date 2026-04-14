package codientcli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"codient/internal/planstore"
)

// ApprovalDecision represents the user's structured response to a plan.
type ApprovalDecision struct {
	Action   string // "approve", "reject", "edit", "continue"
	Feedback string // non-empty for reject
}

// promptApproval renders the plan and presents structured options.
// Returns the user's decision. Requires an interactive scanner.
func (s *session) promptApproval(sc *bufio.Scanner, plan *planstore.Plan) ApprovalDecision {
	fmt.Fprintf(os.Stderr, "\n%s", planstore.RenderMarkdown(plan))
	fmt.Fprintf(os.Stderr, "\n  [a] Approve and build\n")
	fmt.Fprintf(os.Stderr, "  [r] Reject with feedback\n")
	fmt.Fprintf(os.Stderr, "  [e] Edit plan (open in $EDITOR)\n")
	fmt.Fprintf(os.Stderr, "  [c] Continue planning\n")
	fmt.Fprintf(os.Stderr, "\ncodient: choose action: ")

	if !sc.Scan() {
		return ApprovalDecision{Action: "continue"}
	}
	choice := strings.ToLower(strings.TrimSpace(sc.Text()))

	switch {
	case choice == "a" || choice == "approve":
		return ApprovalDecision{Action: "approve"}
	case choice == "r" || choice == "reject":
		return s.promptRejectFeedback(sc)
	case choice == "e" || choice == "edit":
		return s.promptEditPlan(plan)
	default:
		return ApprovalDecision{Action: "continue"}
	}
}

func (s *session) promptRejectFeedback(sc *bufio.Scanner) ApprovalDecision {
	fmt.Fprintf(os.Stderr, "codient: enter feedback (then press Enter): ")
	if !sc.Scan() {
		return ApprovalDecision{Action: "reject", Feedback: ""}
	}
	fb := strings.TrimSpace(sc.Text())
	return ApprovalDecision{Action: "reject", Feedback: fb}
}

func (s *session) promptEditPlan(plan *planstore.Plan) ApprovalDecision {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "vi"
		}
	}

	md := planstore.RenderMarkdown(plan)
	tmpFile, err := os.CreateTemp("", "codient-plan-*.md")
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: cannot create temp file: %v\n", err)
		return ApprovalDecision{Action: "continue"}
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(md); err != nil {
		tmpFile.Close()
		fmt.Fprintf(os.Stderr, "codient: write temp: %v\n", err)
		return ApprovalDecision{Action: "continue"}
	}
	tmpFile.Close()

	cmd := exec.Command(editor, tmpPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "codient: editor exited with error: %v\n", err)
		return ApprovalDecision{Action: "continue"}
	}

	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: read edited file: %v\n", err)
		return ApprovalDecision{Action: "continue"}
	}

	editedMd := string(edited)
	if editedMd == md {
		fmt.Fprintf(os.Stderr, "codient: no changes detected\n")
		return ApprovalDecision{Action: "continue"}
	}

	reparsed := planstore.ParseFromMarkdown(editedMd, plan.UserRequest)
	plan.Summary = reparsed.Summary
	plan.Steps = reparsed.Steps
	plan.Assumptions = reparsed.Assumptions
	plan.OpenQuestions = reparsed.OpenQuestions
	plan.FilesToModify = reparsed.FilesToModify
	plan.Verification = reparsed.Verification
	plan.RawMarkdown = reparsed.RawMarkdown
	planstore.IncrementRevision(plan)

	if err := planstore.Save(plan); err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "codient: plan updated to revision %d\n", plan.Revision)
	return ApprovalDecision{Action: "edit"}
}

// recordApproval sets the approval metadata on the plan and saves it.
func recordApproval(plan *planstore.Plan, decision, feedback string) {
	plan.Approval = &planstore.Approval{
		Decision:  decision,
		Feedback:  feedback,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}
