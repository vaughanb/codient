package assistout

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrepareAssistantText_PlanMode(t *testing.T) {
	in := "Pick one?\n\nA) a B) b\n\nWaiting for your answer"
	got := PrepareAssistantText(in, true)
	if !strings.Contains(got, "- A) a") || strings.Contains(got, "a B)") {
		t.Fatalf("expected list normalization: %q", got)
	}
	if PrepareAssistantText("x", false) != "x" {
		t.Fatal("non-plan should trim only")
	}
}

func TestWriteAssistant_Plain(t *testing.T) {
	var buf bytes.Buffer
	err := WriteAssistant(&buf, "# Title\n\nHello", false, false)
	if err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, "# Title") {
		t.Fatalf("expected raw markdown, got %q", s)
	}
}

func TestWriteAssistant_EmptyPlain(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAssistant(&buf, "", false, false); err != nil {
		t.Fatal(err)
	}
}

func TestWriteWelcome_Plain(t *testing.T) {
	t.Setenv("CODIENT_QUIET", "")
	var buf bytes.Buffer
	WriteWelcome(&buf, WelcomeParams{
		Plain:     true,
		Repl:      true,
		Mode:      "plan",
		Workspace: "/tmp/ws",
		Model:     "m1",
	})
	s := buf.String()
	if !strings.Contains(s, "codient") || !strings.Contains(s, "REPL") || !strings.Contains(s, "plan") {
		t.Fatalf("unexpected welcome: %q", s)
	}
}

func TestWriteWelcome_Quiet(t *testing.T) {
	t.Setenv("CODIENT_QUIET", "1")
	var buf bytes.Buffer
	WriteWelcome(&buf, WelcomeParams{Plain: true, Mode: "agent"})
	if buf.Len() != 0 {
		t.Fatalf("expected empty, got %q", buf.String())
	}
}

func TestPlanStdinPrompt_Plain(t *testing.T) {
	if !strings.HasPrefix(PlanStdinPrompt(true, ""), "Message:") {
		t.Fatalf("first line: %q", PlanStdinPrompt(true, ""))
	}
	if !strings.HasPrefix(PlanStdinPrompt(true, "x **Waiting for your answer**"), "Answer:") {
		t.Fatalf("wait: %q", PlanStdinPrompt(true, "x **Waiting for your answer**"))
	}
	if !strings.HasPrefix(PlanStdinPrompt(true, "Ready to implement only."), "Follow-up") {
		t.Fatalf("follow-up: %q", PlanStdinPrompt(true, "Ready to implement only."))
	}
}
