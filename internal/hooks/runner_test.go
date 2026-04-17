package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunHookCommand_ScrubsEnv(t *testing.T) {
	const leak = "secret-token-must-not-appear-in-child"
	t.Setenv("GITHUB_TOKEN", leak)
	dir := t.TempDir()
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = `echo %GITHUB_TOKEN%`
	} else {
		cmd = `printf '%s' "$GITHUB_TOKEN"`
	}
	rr := runHookCommand(context.Background(), dir, cmd, 30, nil)
	if rr.Err != nil {
		t.Fatalf("runHookCommand: %v", rr.Err)
	}
	if strings.Contains(string(rr.Stdout), leak) || strings.Contains(string(rr.Stderr), leak) {
		t.Fatalf("GITHUB_TOKEN leaked into hook subprocess stdout/stderr")
	}
}

func TestParseHookOutput_exit2(t *testing.T) {
	t.Parallel()
	out, err := parseHookOutput(nil, []byte("nope"), exitCodeBlock)
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "block" {
		t.Fatalf("decision %q", out.Decision)
	}
}

func TestParseHookOutput_jsonBlock(t *testing.T) {
	t.Parallel()
	stdout := []byte(`{"decision":"block","reason":"no"}`)
	out, err := parseHookOutput(stdout, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(out.Decision, "block") || out.Reason != "no" {
		t.Fatalf("%+v", out)
	}
}

func TestRunMatchingHandlers_preToolBlock(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("use cmd built-ins only in this quick test")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "block.sh")
	body := `#!/bin/sh
printf '{"decision":"block","reason":"denied"}'
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	groups := []MatcherGroup{{
		Matcher: "write_file",
		Hooks: []Handler{{
			Type:    "command",
			Command: script,
		}},
	}}
	env := map[string]any{
		"hook_event_name": EventPreToolUse,
		"tool_name":       "write_file",
	}
	out, err := runMatchingHandlers(context.Background(), dir, groups, "write_file", env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(out.Decision, "block") {
		t.Fatalf("got %+v", out)
	}
}

func noopHookCommand() string {
	if runtime.GOOS == "windows" {
		return "exit /b 0"
	}
	return "true"
}

func TestRunPreToolUse_allow(t *testing.T) {
	t.Parallel()
	loaded := &Loaded{
		ByEvent: map[string][]MatcherGroup{
			EventPreToolUse: {{
				Matcher: "echo",
				Hooks: []Handler{{
					Type:    "command",
					Command: noopHookCommand(),
				}},
			}},
		},
	}
	m := NewManager(loaded, t.TempDir(), "m", "sid")
	res, err := m.RunPreToolUse(context.Background(), "echo", json.RawMessage(`{"message":"x"}`), "id1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Allow {
		t.Fatalf("blocked: %+v", res)
	}
}
