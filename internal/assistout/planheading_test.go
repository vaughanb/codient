package assistout

import (
	"strings"
	"testing"
)

func TestReplySignalsPlanWait(t *testing.T) {
	if !ReplySignalsPlanWait("foo\n**Waiting for your answer**\n") {
		t.Fatal("expected true")
	}
	if !ReplySignalsPlanWait("foo\nWaiting for your answer\n") {
		t.Fatal("expected true for plain wait phrase")
	}
	if ReplySignalsPlanWait("Ready to implement.\nNo questions.") {
		t.Fatal("expected false")
	}
}

func TestInsertPlanQuestionHeading_NoOp(t *testing.T) {
	if got := InsertPlanQuestionHeading("no wait phrase"); got != "no wait phrase" {
		t.Fatalf("got %q", got)
	}
	s := "## Question\n\nPick one?\n\n**Waiting for your answer**"
	if got := InsertPlanQuestionHeading(s); got != s {
		t.Fatalf("should not duplicate heading: %q", got)
	}
}

func TestInsertPlanQuestionHeading_Inserts(t *testing.T) {
	in := "Plan prose here.\n\nPick storage?\n**A)** JSON **B)** SQLite\n\n**Waiting for your answer**"
	got := InsertPlanQuestionHeading(in)
	if !strings.Contains(got, "## Question\n\n") {
		t.Fatalf("missing heading: %q", got)
	}
	after := strings.Split(got, "## Question\n\n")[1]
	if !strings.HasPrefix(strings.TrimSpace(after), "Pick storage?") {
		t.Fatalf("expected question body after heading: %q", after)
	}
}

func TestNormalizePlanQuestionOptionLines_NoWait(t *testing.T) {
	s := "A) one B) two"
	if got := NormalizePlanQuestionOptionLines(s); got != s {
		t.Fatalf("expected no-op: %q", got)
	}
}

func TestNormalizePlanQuestionOptionLines_PlainPacked(t *testing.T) {
	in := "## Question\n\nPick interface:\n\nA) CLI B) HTTP C) TUI D) Other\n\n**Waiting for your answer**"
	got := NormalizePlanQuestionOptionLines(in)
	if strings.Contains(got, "A) CLI B)") {
		t.Fatalf("expected B) on new line: %q", got)
	}
	for _, sub := range []string{"- A) CLI", "\n\n- B) HTTP", "\n\n- C) TUI", "\n\n- D) Other"} {
		if !strings.Contains(got, sub) {
			t.Fatalf("missing %q in %q", sub, got)
		}
	}
}

func TestNormalizePlanQuestionOptionLines_BoldPacked(t *testing.T) {
	in := "Q?\n\n**A)** JSON **B)** SQLite **C)** mem\n\n**Waiting for your answer**"
	got := NormalizePlanQuestionOptionLines(in)
	if strings.Contains(got, "JSON **B)**") {
		t.Fatalf("expected newline before **B)**: %q", got)
	}
}

func TestNormalizePlanQuestionOptionLines_PlainWaitPhrase(t *testing.T) {
	in := "## Question\n\nWhat persistence?\n\nA) mem B) file C) db D) other\n\nWaiting for your answer"
	got := NormalizePlanQuestionOptionLines(in)
	if strings.Contains(got, "mem B)") {
		t.Fatalf("expected split before B): %q", got)
	}
}

func TestNormalizePlanQuestionOptionLines_MultilineOptionsPlainWait(t *testing.T) {
	in := "What persistence?\n\nA) In-memory (simple) B) JSON file (portable, human-\nreadable,\ngood tools) C) SQLite D) Other\n\nWaiting for your answer"
	got := NormalizePlanQuestionOptionLines(in)
	for _, sub := range []string{"simple) B)", "tools) C)", "SQLite D)"} {
		if strings.Contains(got, sub) {
			t.Fatalf("still packed %q in %q", sub, got)
		}
	}
}

func TestNormalizePlanQuestionOptionLines_DemoteHeadingOptions(t *testing.T) {
	in := "## Question\n\nPersist how?\n\n- A) mem\n\n## B) JSON\n## C) SQL\nD) other\n\nWaiting for your answer"
	got := NormalizePlanQuestionOptionLines(in)
	if strings.Contains(got, "## B)") || strings.Contains(got, "## C)") {
		t.Fatalf("expected ## removed from options: %q", got)
	}
	if !strings.Contains(got, "- B) JSON") || !strings.Contains(got, "- C) SQL") || !strings.Contains(got, "- D) other") {
		t.Fatalf("expected list markers: %q", got)
	}
}

func TestNormalizePlanQuestionOptionLines_StripEmptyBulletLine(t *testing.T) {
	in := "## Question\n\n- A) one\n-\n- B) two\n\nWaiting for your answer"
	got := NormalizePlanQuestionOptionLines(in)
	if strings.Contains(got, "\n-\n") {
		t.Fatalf("stray empty bullet should be removed: %q", got)
	}
}
