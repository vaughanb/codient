package codientcli

import (
	"strings"
	"testing"

	"codient/internal/planstore"
)

func TestBuildPlanExecutionMessage_IncludesSteps(t *testing.T) {
	plan := &planstore.Plan{
		Summary:       "Add version command.",
		FilesToModify: []string{"main.go", "cmd.go"},
		Steps: []planstore.Step{
			{ID: "s1", Title: "Add const", Description: "define VERSION"},
			{ID: "s2", Title: "Register cmd"},
		},
		Verification: []string{"run /version"},
	}
	msg := buildPlanExecutionMessage(plan, []string{"read_file", "write_file"})
	for _, want := range []string{
		"build mode",
		"read_file",
		"Add version command.",
		"`main.go`",
		"[s1] Add const",
		"define VERSION",
		"[s2] Register cmd",
		"run /version",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in message", want)
		}
	}
}

func TestBuildPlanExecutionMessage_WithFeedback(t *testing.T) {
	plan := &planstore.Plan{
		Steps:    []planstore.Step{{ID: "s1", Title: "Do thing"}},
		Approval: &planstore.Approval{Decision: "approve", Feedback: "add error handling too"},
	}
	msg := buildPlanExecutionMessage(plan, []string{"read_file"})
	if !strings.Contains(msg, "add error handling too") {
		t.Error("feedback not included in message")
	}
}

func TestBuildPlanExecutionMessage_EmptyPlan(t *testing.T) {
	plan := &planstore.Plan{}
	msg := buildPlanExecutionMessage(plan, []string{"read_file"})
	if !strings.Contains(msg, "build mode") {
		t.Error("missing build mode header")
	}
}

func TestBuildPhaseGroupMessage_Structure(t *testing.T) {
	plan := &planstore.Plan{
		Summary: "Big refactor.",
		Steps: []planstore.Step{
			{ID: "s1", Title: "Step A", PhaseGroup: 0, Status: planstore.StepDone},
			{ID: "s2", Title: "Step B", PhaseGroup: 1, Status: planstore.StepPending, Description: "do B things"},
			{ID: "s3", Title: "Step C", PhaseGroup: 2, Status: planstore.StepPending},
		},
		Approval: &planstore.Approval{Decision: "approve", Feedback: "be careful"},
	}
	msg := buildPhaseGroupMessage(plan, 1, []string{"read_file", "write_file"})
	for _, want := range []string{
		"Phase group 2",
		"(completed)",
		"(implement now)",
		"(later)",
		"[done] Step A",
		"[s2] Step B",
		"do B things",
		"[pending] Step C",
		"be careful",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in phase group message", want)
		}
	}
}

func TestMarkGroupInProgress(t *testing.T) {
	plan := &planstore.Plan{Steps: []planstore.Step{
		{ID: "s1", PhaseGroup: 0, Status: planstore.StepDone},
		{ID: "s2", PhaseGroup: 1, Status: planstore.StepPending},
		{ID: "s3", PhaseGroup: 1, Status: planstore.StepPending},
		{ID: "s4", PhaseGroup: 2, Status: planstore.StepPending},
	}}
	markGroupInProgress(plan, 1)
	if plan.Steps[0].Status != planstore.StepDone {
		t.Error("group 0 step should be unchanged")
	}
	if plan.Steps[1].Status != planstore.StepInProgress {
		t.Error("group 1 step should be in_progress")
	}
	if plan.Steps[2].Status != planstore.StepInProgress {
		t.Error("group 1 step should be in_progress")
	}
	if plan.Steps[3].Status != planstore.StepPending {
		t.Error("group 2 step should be unchanged")
	}
}

func TestMarkGroupDone(t *testing.T) {
	plan := &planstore.Plan{Steps: []planstore.Step{
		{ID: "s1", PhaseGroup: 0, Status: planstore.StepInProgress},
		{ID: "s2", PhaseGroup: 0, Status: planstore.StepDone},
		{ID: "s3", PhaseGroup: 1, Status: planstore.StepInProgress},
	}}
	markGroupDone(plan, 0)
	if plan.Steps[0].Status != planstore.StepDone {
		t.Error("in_progress step should become done")
	}
	if plan.Steps[1].Status != planstore.StepDone {
		t.Error("already done step should remain done")
	}
	if plan.Steps[2].Status != planstore.StepInProgress {
		t.Error("other group step should be unchanged")
	}
}
