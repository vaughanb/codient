package codientcli

import (
	"os"
	"strings"
	"testing"

	"codient/internal/config"
)

// captureStderr redirects os.Stderr to a pipe for the duration of fn,
// then returns everything written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	fn()

	w.Close()
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	r.Close()
	return string(buf[:n])
}

func TestReplAsyncStderrNote_DefersWhenInputActive(t *testing.T) {
	s := &session{}
	s.replInputActive = true

	out := captureStderr(t, func() {
		s.replAsyncStderrNote("index ready\n")
	})

	if out != "" {
		t.Fatalf("expected no stderr output while input active, got %q", out)
	}
	if len(s.pendingAsyncNotes) != 1 || s.pendingAsyncNotes[0] != "index ready\n" {
		t.Fatalf("expected one pending note, got %v", s.pendingAsyncNotes)
	}
}

func TestReplAsyncStderrNote_PrintsWhenInputInactive(t *testing.T) {
	s := &session{cfg: &config.Config{Plain: true}}

	out := captureStderr(t, func() {
		s.replAsyncStderrNote("index ready\n")
	})

	if !strings.Contains(out, "index ready") {
		t.Fatalf("expected note in stderr output, got %q", out)
	}
	if len(s.pendingAsyncNotes) != 0 {
		t.Fatalf("expected no pending notes, got %v", s.pendingAsyncNotes)
	}
}

func TestReplFlushPendingNotes_PrintsAndClears(t *testing.T) {
	s := &session{}
	s.replInputActive = true
	s.pendingAsyncNotes = []string{"note 1\n", "note 2\n"}

	out := captureStderr(t, func() {
		s.replFlushPendingNotes()
	})

	if !strings.Contains(out, "note 1") || !strings.Contains(out, "note 2") {
		t.Fatalf("expected both notes in output, got %q", out)
	}
	if s.replInputActive {
		t.Fatal("expected replInputActive to be false after flush")
	}
	if len(s.pendingAsyncNotes) != 0 {
		t.Fatalf("expected pendingAsyncNotes cleared, got %v", s.pendingAsyncNotes)
	}
}

func TestReplFlushPendingNotes_NoopWhenEmpty(t *testing.T) {
	s := &session{}
	s.replInputActive = true

	out := captureStderr(t, func() {
		s.replFlushPendingNotes()
	})

	if out != "" {
		t.Fatalf("expected no stderr output for empty flush, got %q", out)
	}
	if s.replInputActive {
		t.Fatal("expected replInputActive to be false after flush")
	}
}

func TestReplAsyncStderrNote_Empty(t *testing.T) {
	s := &session{}
	s.replInputActive = true

	s.replAsyncStderrNote("")
	if len(s.pendingAsyncNotes) != 0 {
		t.Fatal("empty note should be ignored")
	}
}

func TestReplAsyncStderrNote_MultipleDeferredNotes(t *testing.T) {
	s := &session{}
	s.replInputActive = true

	s.replAsyncStderrNote("first\n")
	s.replAsyncStderrNote("second\n")

	if len(s.pendingAsyncNotes) != 2 {
		t.Fatalf("expected 2 pending notes, got %d", len(s.pendingAsyncNotes))
	}

	out := captureStderr(t, func() {
		s.replFlushPendingNotes()
	})

	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("expected both notes flushed, got %q", out)
	}
	if idx1, idx2 := strings.Index(out, "first"), strings.Index(out, "second"); idx1 >= idx2 {
		t.Fatal("expected notes flushed in order")
	}
}

func TestReplFlushPendingNotes_AddsTrailingNewline(t *testing.T) {
	s := &session{}
	s.replInputActive = true
	s.pendingAsyncNotes = []string{"no newline"}

	out := captureStderr(t, func() {
		s.replFlushPendingNotes()
	})

	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("expected trailing newline, got %q", out)
	}
}
