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
	var buf bytes.Buffer
	WriteWelcome(&buf, WelcomeParams{
		Plain:     true,
		Repl:      true,
		Mode:      "plan",
		Workspace: "/tmp/ws",
		Model:     "m1",
	})
	s := buf.String()
	if !strings.Contains(s, "█") || !strings.Contains(s, "Session") || !strings.Contains(s, "plan") {
		t.Fatalf("unexpected welcome: %q", s)
	}
	if !strings.Contains(s, "OpenAI-compatible") {
		t.Fatalf("expected OpenAI-compatible tagline: %q", s)
	}
}

func TestCodientBlockASCII(t *testing.T) {
	if len(codientBlockASCII) != 5 {
		t.Fatalf("rows: %d", len(codientBlockASCII))
	}
	var want int
	for i, row := range codientBlockASCII {
		n := len([]rune(row))
		if i == 0 {
			want = n
			continue
		}
		if n != want {
			t.Fatalf("row %d len %d want %d", i, n, want)
		}
	}
}

func TestWriteWelcome_Quiet(t *testing.T) {
	var buf bytes.Buffer
	WriteWelcome(&buf, WelcomeParams{Quiet: true, Plain: true, Mode: "build"})
	if buf.Len() != 0 {
		t.Fatalf("expected empty, got %q", buf.String())
	}
}

func TestWriteWelcome_Quiet_ResumeSummary(t *testing.T) {
	var buf bytes.Buffer
	WriteWelcome(&buf, WelcomeParams{
		Quiet:         true,
		Plain:         true,
		Mode:          "build",
		ResumeSummary: "session x · 1 turn · last: hi",
	})
	s := buf.String()
	if !strings.Contains(s, "resuming") || !strings.Contains(s, "last: hi") {
		t.Fatalf("expected quiet resume line: %q", s)
	}
}

func TestWriteWelcome_Plain_ResumeSummary(t *testing.T) {
	var buf bytes.Buffer
	WriteWelcome(&buf, WelcomeParams{
		Plain:         true,
		Repl:          true,
		Mode:          "build",
		Workspace:     "/tmp",
		Model:         "m",
		ResumeSummary: "session x · 2 turns · last: fix bug",
	})
	s := buf.String()
	if !strings.Contains(s, "Resuming ·") || !strings.Contains(s, "fix bug") {
		t.Fatalf("expected resume in banner: %q", s)
	}
}

func TestFormatResumeSummary_TruncatesAndNormalizesWhitespace(t *testing.T) {
	in := "  one\t two   three   four five six seven eight nine ten eleven twelve thirteen fourteen  "
	out := formatResumeSummary(in, 24)
	if strings.Contains(out, "\t") || strings.Contains(out, "  ") {
		t.Fatalf("expected normalized spacing, got %q", out)
	}
	if len([]rune(out)) > 24 {
		t.Fatalf("expected truncation to <=24 runes, got %d in %q", len([]rune(out)), out)
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatalf("expected trailing ellipsis when truncated, got %q", out)
	}
}

func TestSessionPrompt_Plain(t *testing.T) {
	p := SessionPrompt(true, "build")
	if !strings.HasPrefix(p, "[build] > ") {
		t.Fatalf("unexpected prompt: %q", p)
	}
	p = SessionPrompt(true, "plan")
	if !strings.HasPrefix(p, "[plan] > ") {
		t.Fatalf("unexpected prompt: %q", p)
	}
}

func TestPlanAnswerPrefix_Plain(t *testing.T) {
	p := PlanAnswerPrefix(true)
	if !strings.HasPrefix(p, "Answer:") {
		t.Fatalf("unexpected prefix: %q", p)
	}
}

func TestProgressIntentBulletPrefix_Plain(t *testing.T) {
	want := "  ● "
	if got := ProgressIntentBulletPrefix(true, "plan"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
