package main

import (
	"strings"
	"testing"

	"codient/internal/config"
	"codient/internal/prompt"
)

func TestBuildAgentSystemPrompt_IncludesRunCommandHelp(t *testing.T) {
	cfg := &config.Config{
		Workspace:     "/tmp/w",
		ExecAllowlist: []string{"go", "git"},
	}
	reg := buildRegistry(cfg, prompt.ModeAgent)
	s := buildAgentSystemPrompt(cfg, reg, prompt.ModeAgent, "", "")
	if !strings.Contains(s, "run_command") {
		t.Fatalf("missing run_command: %s", s)
	}
	if !strings.Contains(s, "go, git") && !strings.Contains(s, "go") {
		t.Fatalf("missing allowlist: %s", s)
	}
	if !strings.Contains(s, `"go","test"`) {
		t.Fatalf("missing example: %s", s)
	}
}

func TestBuildAgentSystemPrompt_UserSystemAppended(t *testing.T) {
	cfg := &config.Config{}
	reg := buildRegistry(cfg, prompt.ModeAgent)
	s := buildAgentSystemPrompt(cfg, reg, prompt.ModeAgent, "Be concise.", "")
	if !strings.Contains(s, "Be concise.") {
		t.Fatalf("got %s", s)
	}
}

func TestBuildRegistry_Plan_NoEcho(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir()}
	reg := buildRegistry(cfg, prompt.ModePlan)
	for _, n := range reg.Names() {
		if n == "echo" {
			t.Fatal("plan mode must not register echo")
		}
	}
}

func TestBuildRegistry_Ask_IgnoresExecAllowlist(t *testing.T) {
	cfg := &config.Config{
		Workspace:     t.TempDir(),
		ExecAllowlist: []string{"go"},
	}
	reg := buildRegistry(cfg, prompt.ModeAsk)
	for _, n := range reg.Names() {
		if n == "run_command" || n == "write_file" {
			t.Fatalf("unexpected %q in ask mode", n)
		}
	}
}
