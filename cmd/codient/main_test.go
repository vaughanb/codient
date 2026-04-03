package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"codient/internal/prompt"
)

func TestResolvePrompt_FromFlag(t *testing.T) {
	s, err := resolvePrompt("  hello  ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(s) != "hello" {
		t.Fatalf("got %q", s)
	}
}

func TestResolvePrompt_EmptyFlag(t *testing.T) {
	s, err := resolvePrompt("")
	if err != nil {
		t.Fatal(err)
	}
	if s != "" {
		t.Fatalf("expected empty when no stdin pipe; got %q (TTY?)", s)
	}
}

func TestResolveProgressOut_FlagAndEnv(t *testing.T) {
	t.Setenv("CODIENT_PROGRESS", "")
	if w := resolveProgressOut(true, false); w != os.Stderr {
		t.Fatalf("progress flag: got %v want stderr", w)
	}
	t.Setenv("CODIENT_PROGRESS", "1")
	if w := resolveProgressOut(false, false); w != os.Stderr {
		t.Fatalf("CODIENT_PROGRESS=1: got %v want stderr", w)
	}
	t.Setenv("CODIENT_PROGRESS", "0")
	if w := resolveProgressOut(true, true); w != nil {
		t.Fatalf("CODIENT_PROGRESS=0 should disable even with -progress and log; got %v", w)
	}
	if w := resolveProgressOut(false, true); w != nil {
		t.Fatalf("CODIENT_PROGRESS=0 should disable log default; got %v", w)
	}
	t.Setenv("CODIENT_PROGRESS", "")
	if w := resolveProgressOut(false, true); w != os.Stderr {
		t.Fatalf("log requested: got %v want stderr", w)
	}
}

func TestStreamWriterForTurn_PlanRichOnlyAfterBlockingQuestion(t *testing.T) {
	waiting := "Q?\n\n**Waiting for your answer**"
	if w := streamWriterForTurn(true, true, prompt.ModePlan, true, waiting); w != nil {
		t.Fatalf("plan+rich after blocking question: expected buffered glamour, got %v", w)
	}
	if w := streamWriterForTurn(true, true, prompt.ModePlan, true, "Ready to implement."); w == nil {
		t.Fatal("plan+rich follow-up: expected streaming")
	}
	if w := streamWriterForTurn(true, true, prompt.ModePlan, false, waiting); w == nil {
		t.Fatal("plan+plain after question: expected streaming")
	}
	if w := streamWriterForTurn(true, true, prompt.ModeAgent, true, waiting); w == nil {
		t.Fatal("agent+rich: expected streaming")
	}
}

func TestWritePlanDraftPreamble_AfterBlockingQuestion(t *testing.T) {
	var buf bytes.Buffer
	writePlanDraftPreamble(&buf, prompt.ModeAgent, "x **Waiting for your answer**")
	if buf.Len() != 0 {
		t.Fatalf("non-plan: expected no preamble, got %q", buf.String())
	}
	buf.Reset()
	writePlanDraftPreamble(&buf, prompt.ModePlan, "no wait")
	if buf.Len() != 0 {
		t.Fatalf("no wait phrase: expected empty, got %q", buf.String())
	}
	buf.Reset()
	writePlanDraftPreamble(&buf, prompt.ModePlan, "Q?\n\n**Waiting for your answer**")
	s := buf.String()
	if !strings.Contains(s, "Building the implementation plan") {
		t.Fatalf("expected status line: %q", s)
	}
}

func TestResolveProgressOut_StderrTTYFallback(t *testing.T) {
	t.Setenv("CODIENT_PROGRESS", "")
	st, err := os.Stderr.Stat()
	if err != nil {
		t.Fatal(err)
	}
	isTTY := (st.Mode() & os.ModeCharDevice) != 0
	got := resolveProgressOut(false, false)
	if isTTY && got != os.Stderr {
		t.Fatalf("interactive stderr: got %v want stderr", got)
	}
	if !isTTY && got != nil {
		t.Fatalf("non-interactive stderr: got %v want nil", got)
	}
}
