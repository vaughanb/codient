package tokentracker

import (
	"strings"
	"sync"
	"testing"
)

func TestTracker_AddSession(t *testing.T) {
	var tr Tracker
	tr.Add(Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150})
	tr.Add(Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	s := tr.Session()
	if s.PromptTokens != 110 || s.CompletionTokens != 55 || s.TotalTokens != 165 {
		t.Fatalf("session: %+v", s)
	}
	if l := tr.Last(); l.PromptTokens != 10 {
		t.Fatalf("last: %+v", l)
	}
}

func TestTracker_Reset(t *testing.T) {
	var tr Tracker
	tr.Add(Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
	tr.Reset()
	if tr.Session().HasAny() {
		t.Fatal("expected empty after reset")
	}
}

func TestTracker_TurnMark(t *testing.T) {
	var tr Tracker
	tr.Add(Usage{PromptTokens: 100, CompletionTokens: 0, TotalTokens: 100})
	tr.MarkTurnStart()
	tr.Add(Usage{PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50})
	d := tr.TurnSinceMark()
	if d.PromptTokens != 40 || d.CompletionTokens != 10 {
		t.Fatalf("turn delta: %+v", d)
	}
}

func TestTracker_Concurrent(t *testing.T) {
	var tr Tracker
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.Add(Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
		}()
	}
	wg.Wait()
	s := tr.Session()
	if s.PromptTokens != 100 {
		t.Fatalf("got %d", s.PromptTokens)
	}
}

func TestFormatLine(t *testing.T) {
	if s := FormatLine(Usage{}); s != "" {
		t.Fatalf("empty: %q", s)
	}
	s := FormatLine(Usage{PromptTokens: 1200, CompletionTokens: 340})
	if !strings.Contains(s, "1.20k") || !strings.Contains(s, "340") {
		t.Fatalf("got %q", s)
	}
}

func TestFormatLineCtx(t *testing.T) {
	u := Usage{PromptTokens: 50000, CompletionTokens: 2000}

	// No context window → no percentage.
	s := FormatLineCtx(u, 0)
	if strings.Contains(s, "ctx") {
		t.Fatalf("should not contain ctx with 0 window: %q", s)
	}
	if !strings.Contains(s, "in") {
		t.Fatalf("should still format tokens: %q", s)
	}

	// With context window → shows percentage.
	s = FormatLineCtx(u, 128000)
	if !strings.Contains(s, "39% ctx") {
		t.Fatalf("expected 39%% ctx, got %q", s)
	}

	// 100% usage.
	s = FormatLineCtx(Usage{PromptTokens: 128000, CompletionTokens: 500}, 128000)
	if !strings.Contains(s, "100% ctx") {
		t.Fatalf("expected 100%% ctx, got %q", s)
	}

	// Empty usage → empty string.
	if s := FormatLineCtx(Usage{}, 128000); s != "" {
		t.Fatalf("empty usage should return empty: %q", s)
	}
}
