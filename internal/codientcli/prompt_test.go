package codientcli

import (
	"context"
	"strings"
	"testing"

	"codient/internal/config"
	"codient/internal/prompt"
	"codient/internal/tools"
)

func TestBuildAgentSystemPrompt_IncludesRunCommandHelp(t *testing.T) {
	cfg := &config.Config{
		Workspace:     "/tmp/w",
		ExecAllowlist: []string{"go", "git"},
	}
	reg := buildRegistry(cfg, prompt.ModeBuild, nil, nil)
	s := buildAgentSystemPrompt(cfg, reg, prompt.ModeBuild, "", "", "", "")
	if !strings.Contains(s, "run_command") {
		t.Fatalf("missing run_command: %s", s)
	}
	if !strings.Contains(s, "go, git") && !strings.Contains(s, "go") {
		t.Fatalf("missing allowlist: %s", s)
	}
	if !strings.Contains(s, `"go","test"`) {
		t.Fatalf("missing example: %s", s)
	}
	if !strings.Contains(s, "run_shell") {
		t.Fatalf("missing run_shell: %s", s)
	}
}

func TestBuildAgentSystemPrompt_UserSystemAppended(t *testing.T) {
	cfg := &config.Config{}
	reg := buildRegistry(cfg, prompt.ModeBuild, nil, nil)
	s := buildAgentSystemPrompt(cfg, reg, prompt.ModeBuild, "Be concise.", "", "", "")
	if !strings.Contains(s, "Be concise.") {
		t.Fatalf("got %s", s)
	}
}

func TestBuildRegistry_Plan_NoEcho(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir()}
	reg := buildRegistry(cfg, prompt.ModePlan, nil, nil)
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
	reg := buildRegistry(cfg, prompt.ModeAsk, nil, nil)
	for _, n := range reg.Names() {
		if n == "run_command" || n == "run_shell" || n == "write_file" {
			t.Fatalf("unexpected %q in ask mode", n)
		}
	}
}

func TestBuildRegistry_NilSession_NoDelegateTask(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir()}
	for _, mode := range []prompt.Mode{prompt.ModeBuild, prompt.ModeAsk, prompt.ModePlan} {
		reg := buildRegistry(cfg, mode, nil, nil)
		for _, n := range reg.Names() {
			if n == "delegate_task" {
				t.Fatalf("nil session (sub-agent) should NOT have delegate_task, mode=%s", mode)
			}
		}
	}
}

func TestBuildAgentSystemPrompt_DelegationSection_Build(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir()}
	reg := buildRegistry(cfg, prompt.ModeBuild, nil, nil)
	tools.RegisterDelegateTask(reg, "build", func(_ context.Context, _, _, _ string) (string, error) {
		return "", nil
	})
	s := buildAgentSystemPrompt(cfg, reg, prompt.ModeBuild, "", "", "", "")
	if !strings.Contains(s, "Task delegation") {
		t.Fatal("build mode prompt should include Task delegation section")
	}
	if !strings.Contains(s, "build") && !strings.Contains(s, "plan") {
		t.Fatal("build mode delegation should mention build and plan sub-agents")
	}
}

func TestBuildAgentSystemPrompt_DelegationSection_Ask(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir()}
	reg := buildRegistry(cfg, prompt.ModeAsk, nil, nil)
	tools.RegisterDelegateTask(reg, "ask", func(_ context.Context, _, _, _ string) (string, error) {
		return "", nil
	})
	s := buildAgentSystemPrompt(cfg, reg, prompt.ModeAsk, "", "", "", "")
	if !strings.Contains(s, "Task delegation") {
		t.Fatal("ask mode prompt should include Task delegation section")
	}
	if !strings.Contains(s, "read-only") {
		t.Fatal("ask mode delegation should mention read-only")
	}
}

func TestBuildAgentSystemPrompt_NoDelegation_WithoutTool(t *testing.T) {
	cfg := &config.Config{Workspace: t.TempDir()}
	reg := buildRegistry(cfg, prompt.ModeBuild, nil, nil)
	s := buildAgentSystemPrompt(cfg, reg, prompt.ModeBuild, "", "", "", "")
	if strings.Contains(s, "Task delegation") {
		t.Fatal("system prompt should NOT include Task delegation when delegate_task is not registered")
	}
}
