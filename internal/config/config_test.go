package config

import (
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("LMSTUDIO_BASE_URL", "")
	t.Setenv("LMSTUDIO_API_KEY", "")
	t.Setenv("LMSTUDIO_MODEL", "")
	t.Setenv("AGENT_MAX_TOOL_STEPS", "")
	t.Setenv("LLM_MAX_CONCURRENT", "")
	t.Setenv("CODIENT_WORKSPACE", "")
	t.Setenv("CODIENT_READ_FILE_ROOT", "")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != defaultBaseURL {
		t.Fatalf("BaseURL: got %q", c.BaseURL)
	}
	if c.APIKey != defaultAPIKey {
		t.Fatalf("APIKey: got %q", c.APIKey)
	}
	if c.MaxToolSteps != defaultMaxToolSteps {
		t.Fatalf("MaxToolSteps: got %d", c.MaxToolSteps)
	}
	if c.MaxConcurrent != defaultMaxConcurrent {
		t.Fatalf("MaxConcurrent: got %d", c.MaxConcurrent)
	}
}

func TestLoad_CustomEnv(t *testing.T) {
	t.Setenv("LMSTUDIO_BASE_URL", "http://example.com/v1/")
	t.Setenv("LMSTUDIO_API_KEY", "secret")
	t.Setenv("LMSTUDIO_MODEL", "m1")
	t.Setenv("AGENT_MAX_TOOL_STEPS", "5")
	t.Setenv("LLM_MAX_CONCURRENT", "2")
	t.Setenv("CODIENT_WORKSPACE", "/w")
	t.Setenv("CODIENT_READ_FILE_ROOT", "/legacy")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://example.com/v1" {
		t.Fatalf("BaseURL trim: got %q", c.BaseURL)
	}
	if c.APIKey != "secret" || c.Model != "m1" {
		t.Fatalf("credentials: %+v", c)
	}
	if c.MaxToolSteps != 5 || c.MaxConcurrent != 2 {
		t.Fatalf("limits: %+v", c)
	}
	if c.Workspace != "/w" || c.ReadFileRoot != "/legacy" {
		t.Fatalf("paths: %+v", c)
	}
}

func TestLoad_InvalidMaxToolSteps(t *testing.T) {
	t.Setenv("AGENT_MAX_TOOL_STEPS", "0")
	t.Setenv("LLM_MAX_CONCURRENT", "1")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_InvalidMaxConcurrent(t *testing.T) {
	t.Setenv("AGENT_MAX_TOOL_STEPS", "1")
	t.Setenv("LLM_MAX_CONCURRENT", "0")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRequireModel(t *testing.T) {
	c := &Config{Model: ""}
	if err := c.RequireModel(); err == nil {
		t.Fatal("expected error")
	}
	c.Model = "x"
	if err := c.RequireModel(); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_ExecAllowlist(t *testing.T) {
	t.Setenv("CODIENT_EXEC_ALLOWLIST", "go, Git ,GO.exe, git")
	t.Setenv("CODIENT_EXEC_TIMEOUT_SEC", "45")
	t.Setenv("CODIENT_EXEC_MAX_OUTPUT_BYTES", "4096")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ExecAllowlist) != 2 {
		t.Fatalf("deduped allowlist: %#v", c.ExecAllowlist)
	}
	if c.ExecAllowlist[0] != "go" || c.ExecAllowlist[1] != "git" {
		t.Fatalf("order/content: %#v", c.ExecAllowlist)
	}
	if c.ExecTimeoutSeconds != 45 || c.ExecMaxOutputBytes != 4096 {
		t.Fatalf("exec limits: timeout=%d out=%d", c.ExecTimeoutSeconds, c.ExecMaxOutputBytes)
	}
}

func TestLoad_ExecTimeoutClamp(t *testing.T) {
	t.Setenv("CODIENT_EXEC_TIMEOUT_SEC", "999999")
	t.Setenv("CODIENT_EXEC_MAX_OUTPUT_BYTES", "999999999")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ExecTimeoutSeconds != maxExecTimeoutSec {
		t.Fatalf("timeout clamp: got %d", c.ExecTimeoutSeconds)
	}
	if c.ExecMaxOutputBytes != maxExecMaxOutputBytes {
		t.Fatalf("output clamp: got %d", c.ExecMaxOutputBytes)
	}
}

func TestEffectiveWorkspace(t *testing.T) {
	c := &Config{Workspace: "/a", ReadFileRoot: "/b"}
	if c.EffectiveWorkspace() != "/a" {
		t.Fatalf("got %q", c.EffectiveWorkspace())
	}
	c.Workspace = ""
	if c.EffectiveWorkspace() != "/b" {
		t.Fatalf("legacy: got %q", c.EffectiveWorkspace())
	}
}
