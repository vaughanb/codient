package agent

import (
	"strings"
	"testing"
	"time"
)

func TestProgressToolLine_runCommand(t *testing.T) {
	s := ProgressToolLine("run_command", []byte(`{"argv":["go","test","./..."],"cwd":"."}`))
	if s == "" {
		t.Fatal("empty")
	}
	if !strings.Contains(s, "go") || !strings.Contains(s, "test") {
		t.Fatalf("got %q", s)
	}
}

func TestProgressToolLine_readFile(t *testing.T) {
	s := ProgressToolLine("read_file", []byte(`{"path":"cmd/main.go"}`))
	if !strings.Contains(s, "main.go") {
		t.Fatalf("got %q", s)
	}
}

func TestProgressToolCompact_listDirRoot(t *testing.T) {
	s := ProgressToolCompact("list_dir", []byte(`{"path":"."}`))
	if s != "list_dir" {
		t.Fatalf("got %q want list_dir", s)
	}
}

func TestProgressToolCompact_webSearch(t *testing.T) {
	s := ProgressToolCompact("web_search", []byte(`{"query":"go slog handler"}`))
	if !strings.Contains(s, "web_search") || !strings.Contains(s, "go slog handler") {
		t.Fatalf("got %q", s)
	}
}

func TestProgressToolLine_webSearch(t *testing.T) {
	s := ProgressToolLine("web_search", []byte(`{"query":"react hooks tutorial"}`))
	if !strings.Contains(s, "react hooks tutorial") {
		t.Fatalf("got %q", s)
	}
}

func TestProgressToolIntentLine_webSearch(t *testing.T) {
	got := ProgressToolIntentLine("web_search", []byte(`{"query":"exponential backoff"}`))
	if !strings.HasPrefix(got, "    ▸ ") {
		t.Fatalf("want nested indent + tool prelude (no mode ●): %q", got)
	}
	if !strings.Contains(got, "searching the web") {
		t.Fatalf("want web search lead-in: %q", got)
	}
	if !strings.Contains(got, "exponential backoff") {
		t.Fatalf("missing query: %q", got)
	}
	if strings.Contains(got, "I'll") || strings.Contains(got, "please perform") {
		t.Fatalf("unexpected phrasing: %q", got)
	}
}

func TestProgressToolIntentLine_readFile(t *testing.T) {
	got := ProgressToolIntentLine("read_file", []byte(`{"path":"main.go"}`))
	if strings.Contains(got, "I'll") {
		t.Fatalf("should not use first-person lead-in: %q", got)
	}
	if !strings.Contains(got, "reading main.go") {
		t.Fatalf("got %q", got)
	}
}

func TestFormatThinkingProgressLine_plain(t *testing.T) {
	got := FormatThinkingProgressLine(true, "plan", "I'll verify the approach.")
	if !strings.HasPrefix(got, "  ● ") || !strings.Contains(got, "verify") {
		t.Fatalf("got %q", got)
	}
}

func TestFormatThinkingProgressLine_empty(t *testing.T) {
	if FormatThinkingProgressLine(true, "build", "") != "" {
		t.Fatal("expected empty")
	}
}

func TestFormatSyntheticIntentThinkingLine_webSearch(t *testing.T) {
	got := FormatSyntheticIntentThinkingLine(true, "ask", "web_search", []byte(`{"query":"Go 1.26 release"}`))
	if !strings.HasPrefix(got, "  ● ") || !strings.Contains(got, "I'll search the web") {
		t.Fatalf("got %q", got)
	}
}

func TestFormatStatusProgressLine_plain(t *testing.T) {
	got := FormatStatusProgressLine(true, "ask", "verifying suggestions…")
	if !strings.HasPrefix(got, "  ● ") || !strings.Contains(got, "verifying suggestions") {
		t.Fatalf("got %q", got)
	}
}

func TestFormatStatusProgressLine_empty(t *testing.T) {
	if FormatStatusProgressLine(true, "build", "  ") != "" {
		t.Fatal("expected empty")
	}
}

func TestFormatThinkingLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"whitespace", "   \n  ", ""},
		{"simple", "I'll read the config file.", "I'll read the config file."},
		{"multiline short", "Reading config.\nThen updating.", "Reading config. Then updating."},
		{
			"truncates long",
			strings.Repeat("a", 300),
			strings.Repeat("a", 197) + "...",
		},
		{
			"strips XML tool markup",
			"Let me check the file.\n<function=read_file><parameter=path>foo.go</parameter></function>",
			"Let me check the file.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatThinkingLine(tc.input)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatProgressDur(t *testing.T) {
	if formatProgressDur(0) != "1ms" {
		t.Fatalf("zero: %q", formatProgressDur(0))
	}
	if formatProgressDur(420*time.Millisecond) != "420ms" {
		t.Fatalf("ms: %q", formatProgressDur(420*time.Millisecond))
	}
	if formatProgressDur(4800*time.Millisecond) != "4.80s" {
		t.Fatalf("s: %q", formatProgressDur(4800*time.Millisecond))
	}
}
