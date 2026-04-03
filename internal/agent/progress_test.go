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
