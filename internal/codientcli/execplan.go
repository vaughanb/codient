package codientcli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"codient/internal/assistout"
	"codient/internal/planstore"
	"codient/internal/prompt"
)

// executeFromPlan switches to build mode and runs the agent with the approved
// plan as structured execution input. For plans with multiple phase groups,
// it pauses after each group for a checkpoint prompt.
func (s *session) executeFromPlan(ctx context.Context, plan *planstore.Plan) error {
	plan.Phase = planstore.PhaseExecuting
	s.planPhase = planstore.PhaseExecuting
	if err := planstore.Save(plan); err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
	}

	s.switchMode(prompt.ModeBuild)
	s.history = nil

	groups := planstore.StepsByPhaseGroup(plan)
	if len(groups) <= 1 {
		return s.executeAllSteps(ctx, plan)
	}

	for gi, group := range groups {
		if planstore.PhaseGroupDone(group) {
			continue
		}

		markGroupInProgress(plan, gi)
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}

		userMsg := buildPhaseGroupMessage(plan, gi, s.registry.Names())
		runner := s.newRunner()
		preModified, preUntracked := s.captureSnapshot()
		histLen := len(s.history)
		s.turn++
		reply, err := s.executeTurn(ctx, runner, userMsg)
		if err != nil {
			return err
		}
		s.pushUndoIfChanged(preModified, preUntracked, histLen)
		s.lastReply = assistout.PrepareAssistantText(reply, false)

		markGroupDone(plan, gi)
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		s.autoSave()
		s.showGitDiffIfBuild()

		if gi < len(groups)-1 && s.scanner != nil {
			summary := planstore.CheckpointSummary(plan, gi)
			fmt.Fprintf(os.Stderr, "\n%s", summary)
			action := promptCheckpoint(s.scanner)
			switch action {
			case "stop":
				fmt.Fprintf(os.Stderr, "codient: stopped after phase group %d\n", gi+1)
				return nil
			case "replan":
				plan.Phase = planstore.PhaseDraft
				s.planPhase = planstore.PhaseDraft
				planstore.IncrementRevision(plan)
				if err := planstore.Save(plan); err != nil {
					fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
				}
				s.switchMode(prompt.ModePlan)
				fmt.Fprintf(os.Stderr, "codient: switched to plan mode to revise remaining steps\n")
				return nil
			default:
				// continue to next group
			}
		}
	}

	return s.finishExecution(ctx, plan)
}

// executeAllSteps runs the full plan in a single execution turn (for plans
// with one phase group or no grouping).
func (s *session) executeAllSteps(ctx context.Context, plan *planstore.Plan) error {
	for i := range plan.Steps {
		if plan.Steps[i].Status == planstore.StepPending {
			plan.Steps[i].Status = planstore.StepInProgress
		}
	}
	if err := planstore.Save(plan); err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
	}

	runner := s.newRunner()
	userMsg := buildPlanExecutionMessage(plan, s.registry.Names())
	preModified, preUntracked := s.captureSnapshot()
	histLen := len(s.history)
	s.turn++
	reply, err := s.executeTurn(ctx, runner, userMsg)
	if err != nil {
		return err
	}
	s.pushUndoIfChanged(preModified, preUntracked, histLen)
	s.lastReply = assistout.PrepareAssistantText(reply, false)

	for i := range plan.Steps {
		if plan.Steps[i].Status == planstore.StepInProgress {
			plan.Steps[i].Status = planstore.StepDone
		}
	}
	if err := planstore.Save(plan); err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
	}
	s.autoSave()
	s.showGitDiffIfBuild()

	return s.finishExecution(ctx, plan)
}

// finishExecution runs the verification phase if all steps are done.
func (s *session) finishExecution(ctx context.Context, plan *planstore.Plan) error {
	if !planstore.AllStepsDone(plan) {
		return nil
	}
	if s.scanner == nil {
		plan.Phase = planstore.PhaseDone
		s.planPhase = planstore.PhaseDone
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		return nil
	}

	passed, verErr := s.runVerification(ctx, s.scanner, plan)
	if verErr != nil {
		fmt.Fprintf(os.Stderr, "codient: verification error: %v\n", verErr)
	}
	if !passed && plan.Phase == planstore.PhaseExecuting {
		fixRunner := s.newRunner()
		preM, preU := s.captureSnapshot()
		hLen := len(s.history)
		s.turn++
		fixReply, fixErr := s.executeTurn(ctx, fixRunner, "Fix the verification failures described above.")
		if fixErr != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", fixErr)
		} else {
			s.pushUndoIfChanged(preM, preU, hLen)
			s.lastReply = assistout.PrepareAssistantText(fixReply, false)
			s.autoSave()
			s.showGitDiffIfBuild()
		}
	}
	return nil
}

// promptCheckpoint asks the user what to do after a phase group completes.
func promptCheckpoint(sc *bufio.Scanner) string {
	fmt.Fprintf(os.Stderr, "\n  [c] Continue to next phase\n")
	fmt.Fprintf(os.Stderr, "  [p] Re-plan remaining steps\n")
	fmt.Fprintf(os.Stderr, "  [s] Stop here\n")
	fmt.Fprintf(os.Stderr, "\ncodient: choose action: ")

	if !sc.Scan() {
		return "continue"
	}
	choice := strings.ToLower(strings.TrimSpace(sc.Text()))
	switch {
	case choice == "s" || choice == "stop":
		return "stop"
	case choice == "p" || choice == "replan":
		return "replan"
	default:
		return "continue"
	}
}

// markGroupInProgress sets all pending steps in the given phase group to in_progress.
func markGroupInProgress(plan *planstore.Plan, groupIdx int) {
	for i := range plan.Steps {
		if plan.Steps[i].PhaseGroup == groupIdx && plan.Steps[i].Status == planstore.StepPending {
			plan.Steps[i].Status = planstore.StepInProgress
		}
	}
}

// markGroupDone sets all in_progress steps in the given phase group to done.
func markGroupDone(plan *planstore.Plan, groupIdx int) {
	for i := range plan.Steps {
		if plan.Steps[i].PhaseGroup == groupIdx && plan.Steps[i].Status == planstore.StepInProgress {
			plan.Steps[i].Status = planstore.StepDone
		}
	}
}

// buildPhaseGroupMessage constructs a user message for executing a specific
// phase group, providing context about completed and remaining groups.
func buildPhaseGroupMessage(plan *planstore.Plan, groupIdx int, toolNames []string) string {
	var b strings.Builder
	b.WriteString("This session is already in build mode. Available tools: ")
	b.WriteString(strings.Join(toolNames, ", "))
	b.WriteString(". Only use tools from this list.\n\n")

	fmt.Fprintf(&b, "Implement phase group %d of the approved plan. Do not ask for confirmation — start implementing now.\n\n", groupIdx+1)

	b.WriteString("Before implementing each step, verify its premise using tools. ")
	b.WriteString("If a step's premise is wrong, skip it and note why.\n\n")

	if plan.Summary != "" {
		fmt.Fprintf(&b, "## Plan summary\n\n%s\n\n", plan.Summary)
	}

	groups := planstore.StepsByPhaseGroup(plan)
	for gi, group := range groups {
		if gi < groupIdx {
			fmt.Fprintf(&b, "## Phase group %d (completed)\n\n", gi+1)
			for _, step := range group {
				fmt.Fprintf(&b, "- [done] %s\n", step.Title)
			}
			b.WriteString("\n")
		} else if gi == groupIdx {
			fmt.Fprintf(&b, "## Phase group %d (implement now)\n\n", gi+1)
			for i, step := range group {
				fmt.Fprintf(&b, "%d. [%s] %s", i+1, step.ID, step.Title)
				if step.Description != "" {
					fmt.Fprintf(&b, "\n   %s", step.Description)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		} else {
			fmt.Fprintf(&b, "## Phase group %d (later)\n\n", gi+1)
			for _, step := range group {
				fmt.Fprintf(&b, "- [pending] %s\n", step.Title)
			}
			b.WriteString("\n")
		}
	}

	if plan.Approval != nil && plan.Approval.Feedback != "" {
		fmt.Fprintf(&b, "## User feedback\n\n%s\n", plan.Approval.Feedback)
	}

	return b.String()
}

// buildPlanExecutionMessage constructs the user message for the build-mode agent,
// providing structured step information from the approved plan.
func buildPlanExecutionMessage(plan *planstore.Plan, toolNames []string) string {
	var b strings.Builder
	b.WriteString("This session is already in build mode. Available tools: ")
	b.WriteString(strings.Join(toolNames, ", "))
	b.WriteString(". Only use tools from this list.\n\n")

	b.WriteString("The user approved the following implementation plan. Do not ask whether to proceed or for confirmation — they already approved. Start implementing now using tools.\n\n")

	b.WriteString("The plan was produced by a language model in a read-only session. ")
	b.WriteString("Before implementing each step, verify its premise using tools ")
	b.WriteString("(e.g. read the files it references, run existing tests). ")
	b.WriteString("If a step's premise is wrong, skip that step and briefly note why.\n\n")

	if plan.Summary != "" {
		fmt.Fprintf(&b, "## Summary\n\n%s\n\n", plan.Summary)
	}

	if len(plan.FilesToModify) > 0 {
		b.WriteString("## Files to modify\n\n")
		for _, f := range plan.FilesToModify {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}

	if len(plan.Steps) > 0 {
		b.WriteString("## Implementation steps\n\n")
		for i, step := range plan.Steps {
			fmt.Fprintf(&b, "%d. [%s] %s", i+1, step.ID, step.Title)
			if step.Description != "" {
				fmt.Fprintf(&b, "\n   %s", step.Description)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(plan.Verification) > 0 {
		b.WriteString("## Verification\n\nAfter all steps, verify:\n")
		for _, v := range plan.Verification {
			fmt.Fprintf(&b, "- %s\n", v)
		}
		b.WriteString("\n")
	}

	if plan.Approval != nil && plan.Approval.Feedback != "" {
		fmt.Fprintf(&b, "## User feedback\n\n%s\n", plan.Approval.Feedback)
	}

	return b.String()
}
