package prompt

import (
	"testing"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		in   string
		want Mode
	}{
		{"", ModeAgent},
		{"  ", ModeAgent},
		{"agent", ModeAgent},
		{"AGENT", ModeAgent},
		{"ask", ModeAsk},
		{"Plan", ModePlan},
	}
	for _, tc := range tests {
		got, err := ParseMode(tc.in)
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseMode(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
	_, err := ParseMode("nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveMode_FlagOverridesEnv(t *testing.T) {
	t.Setenv("CODIENT_MODE", "ask")
	got, err := ResolveMode("plan")
	if err != nil {
		t.Fatal(err)
	}
	if got != ModePlan {
		t.Fatalf("got %q", got)
	}
}

func TestResolveMode_FromEnv(t *testing.T) {
	t.Setenv("CODIENT_MODE", "ask")
	got, err := ResolveMode("")
	if err != nil {
		t.Fatal(err)
	}
	if got != ModeAsk {
		t.Fatalf("got %q", got)
	}
}
