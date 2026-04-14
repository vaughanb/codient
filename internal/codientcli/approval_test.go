package codientcli

import (
	"testing"
	"time"

	"codient/internal/planstore"
)

func TestRecordApproval(t *testing.T) {
	plan := &planstore.Plan{
		SessionID: "test",
		Steps:     []planstore.Step{{ID: "s1", Title: "do thing"}},
	}
	recordApproval(plan, "approve", "looks great")
	if plan.Approval == nil {
		t.Fatal("Approval should be set")
	}
	if plan.Approval.Decision != "approve" {
		t.Errorf("Decision = %q, want %q", plan.Approval.Decision, "approve")
	}
	if plan.Approval.Feedback != "looks great" {
		t.Errorf("Feedback = %q, want %q", plan.Approval.Feedback, "looks great")
	}
	if plan.Approval.Timestamp == "" {
		t.Error("Timestamp should be set")
	}
	_, err := time.Parse(time.RFC3339, plan.Approval.Timestamp)
	if err != nil {
		t.Errorf("Timestamp not valid RFC3339: %v", err)
	}
}

func TestRecordApproval_Reject(t *testing.T) {
	plan := &planstore.Plan{}
	recordApproval(plan, "reject", "needs more detail")
	if plan.Approval.Decision != "reject" {
		t.Errorf("Decision = %q, want %q", plan.Approval.Decision, "reject")
	}
	if plan.Approval.Feedback != "needs more detail" {
		t.Errorf("Feedback = %q", plan.Approval.Feedback)
	}
}

func TestRecordApproval_EmptyFeedback(t *testing.T) {
	plan := &planstore.Plan{}
	recordApproval(plan, "approve", "")
	if plan.Approval.Feedback != "" {
		t.Errorf("Feedback should be empty, got %q", plan.Approval.Feedback)
	}
}
