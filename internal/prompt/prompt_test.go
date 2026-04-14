package prompt

import (
	"strings"
	"testing"

	"codient/internal/config"
	"codient/internal/tools"
)

func TestBuild_IncludesToolsAndUserSystem(t *testing.T) {
	cfg := &config.Config{Workspace: "/tmp/w"}
	reg := tools.Default("/tmp/w", nil, nil, nil, "", nil)
	s := Build(Params{Cfg: cfg, Reg: reg, Mode: ModeBuild, UserSystem: "custom", RepoInstructions: ""})
	if !strings.Contains(s, "echo") {
		t.Fatalf("missing tool name: %s", s)
	}
	if !strings.Contains(s, "custom") {
		t.Fatalf("missing user system: %s", s)
	}
	if !strings.Contains(s, "## Context") {
		t.Fatalf("missing persona section")
	}
}

func TestBuild_RepoInstructions(t *testing.T) {
	cfg := &config.Config{}
	reg := tools.NewRegistry()
	s := Build(Params{Cfg: cfg, Reg: reg, Mode: ModeBuild, RepoInstructions: "Use tabs."})
	if !strings.Contains(s, "Repository instructions") || !strings.Contains(s, "Use tabs.") {
		t.Fatalf("got %s", s)
	}
}

func TestBuild_ModePlan_IncludesPlanSection(t *testing.T) {
	cfg := &config.Config{Workspace: "/tmp/w"}
	reg := tools.DefaultReadOnlyPlan("/tmp/w", nil, nil, "", nil)
	s := Build(Params{Cfg: cfg, Reg: reg, Mode: ModePlan})
	if !strings.Contains(s, "## Plan mode") || !strings.Contains(s, "Blocking clarification") {
		t.Fatalf("missing plan section: %s", s)
	}
	if !strings.Contains(s, "Required written design") || !strings.Contains(s, "does not remember past runs") {
		t.Fatalf("missing required design guidance: %s", s)
	}
	if !strings.Contains(s, "Underspecified user goal") {
		t.Fatalf("missing question-first guidance: %s", s)
	}
	if !strings.Contains(s, "Ready to implement") {
		t.Fatalf("missing completion guidance: %s", s)
	}
	for _, n := range reg.Names() {
		if n == "echo" {
			t.Fatal("plan registry must omit echo")
		}
	}
	if !strings.Contains(s, "**Plan** mode") {
		t.Fatalf("missing session plan line: %s", s)
	}
}

func TestBuild_ModeAsk_ReadOnlySections(t *testing.T) {
	cfg := &config.Config{Workspace: "/tmp/w"}
	reg := tools.DefaultReadOnly("/tmp/w", nil, nil, "", nil)
	s := Build(Params{Cfg: cfg, Reg: reg, Mode: ModeAsk})
	if !strings.Contains(s, "## Scope (read-only)") || !strings.Contains(s, "**Ask** mode") {
		t.Fatalf("missing ask/read-only: %s", s)
	}
	if strings.Contains(s, "## Plan mode") {
		t.Fatal("ask mode should not include Plan mode section")
	}
}

func TestBuild_AutoCheckNote(t *testing.T) {
	cfg := &config.Config{Workspace: "/tmp/w"}
	reg := tools.Default("/tmp/w", nil, nil, nil, "", nil)
	s := Build(Params{
		Cfg: cfg, Reg: reg, Mode: ModeBuild,
		AutoCheckResolved: "go build ./...",
	})
	if !strings.Contains(s, "Auto-check") || !strings.Contains(s, "go build ./...") || !strings.Contains(s, "[auto-check]") {
		t.Fatalf("expected auto-check note: %s", s)
	}
}
