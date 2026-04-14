//go:build integration

package agent_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"codient/internal/agent"
	"codient/internal/config"
	"codient/internal/openaiclient"
	"codient/internal/tools"
)

// End-to-end agent tests with a live OpenAI-compatible server.
//
// Run:
//
//	CODIENT_INTEGRATION=1 go test -tags=integration ./internal/agent/...
//
// Requires a model configured via /config (or ~/.codient/config.json).
//
// Tool-using tests (anything that requires the model to call a tool) need:
//
//	CODIENT_INTEGRATION_STRICT_TOOLS=1
//
// Model behavior may vary; failures often mean the model ignored tools or the prompt—adjust
// the system string for your model.
//
// The REPL uses streaming for assistant text, but requests that include tools use non-streaming
// completions by default (stream_with_tools false in config) so local servers preserve tool_calls.
// Tests use both nil stream and io.Discard to cover those paths.
//
// Coverage: builtins (echo, get_time), read-only workspace tools (read_file, list_dir, grep,
// search_files), optional run_command (CODIENT_INTEGRATION_RUN_COMMAND=1). write_file and
// str_replace are intentionally omitted (mutating; run a manual check in a throwaway workspace if needed).

func TestIntegration_AgentDirectReply(t *testing.T) {
	skipUnlessIntegration(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	client := openaiclient.New(cfg)
	reg := tools.Default("", nil, nil, nil, "", nil, nil)
	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	reply, _, err := ar.Run(ctx, "You are a helpful assistant.", "Respond with exactly: AGENT_OK", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(reply)) < 4 {
		t.Fatalf("unexpectedly short reply: %q", reply)
	}
	upper := strings.ToUpper(reply)
	if !strings.Contains(upper, "AGENT") && !strings.Contains(upper, "OK") {
		t.Logf("model reply (may still be valid): %q", reply)
	}
	t.Logf("reply: %s", truncateRunes(reply, 500))
}

func TestIntegration_AgentUsesEchoTool(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	ar, ctx, cancel := newLiveRunner(t, "")
	defer cancel()
	const mark = "CODIENT_TOOL_MARK_42"
	sys := "You have a function tool named echo. When the user asks you to echo a message, you MUST call echo with JSON {\"message\": <their exact string>} and nothing else until the tool returns."
	user := "Use the echo tool now with message exactly: " + mark
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, mark) {
		t.Fatalf("expected final reply to contain tool output %q; got: %q", mark, reply)
	}
}

// Exercises the same tool path as an interactive REPL (non-nil stream writer): the agent must
// still use non-streaming completions for tool rounds so tool_calls are not dropped.
func TestIntegration_AgentUsesEchoTool_WithStreamWriter(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	ar, ctx, cancel := newLiveRunner(t, "")
	defer cancel()
	const mark = "CODIENT_STREAM_TOOL_MARK_99"
	sys := "You have a function tool named echo. When the user asks you to echo a message, you MUST call echo with JSON {\"message\": <their exact string>} and nothing else until the tool returns."
	user := "Use the echo tool now with message exactly: " + mark
	reply, _, err := ar.Run(ctx, sys, user, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, mark) {
		t.Fatalf("expected final reply to contain tool output %q; got: %q", mark, reply)
	}
}

func TestIntegration_AgentUsesGetTimeTool(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	ar, ctx, cancel := newLiveRunner(t, "")
	defer cancel()
	sys := "You have a function tool named get_time (no arguments). When the user asks for the time, you MUST call get_time once, then reply in plain text and include the exact RFC3339 timestamp string returned by the tool."
	user := "What is the current time? Call get_time and include the tool result in your answer."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Loose match: full RFC3339 or common variants (with or without fractional seconds / Z).
	rx := regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)
	if !rx.MatchString(reply) {
		t.Fatalf("expected reply to contain an RFC3339-like timestamp; got: %q", truncateRunes(reply, 800))
	}
	t.Logf("reply: %s", truncateRunes(reply, 500))
}

func TestIntegration_AgentUsesEchoTwice(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	ar, ctx, cancel := newLiveRunner(t, "")
	defer cancel()
	const a, b = "CODIENT_DUAL_A_7f3c", "CODIENT_DUAL_B_8e1d"
	sys := "You have a function tool named echo. You MUST call echo exactly twice in order: first {\"message\": \"" + a + "\"}, then {\"message\": \"" + b + "\"}. After both tools return, reply in one short sentence that contains both substrings exactly."
	user := "Perform the two echo calls as instructed in the system message, then answer."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, a) || !strings.Contains(reply, b) {
		t.Fatalf("expected reply to contain both %q and %q; got: %q", a, b, truncateRunes(reply, 1200))
	}
}

func TestIntegration_AgentReadFileWorkspace(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	dir := workspaceFixture(t, map[string]string{
		"codient_read_test.txt": "READ_UNIQUE_CONTENT_4b21c8f3",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	const want = "READ_UNIQUE_CONTENT_4b21c8f3"
	sys := "You have read_file. When asked to read a file, you MUST call read_file with JSON {\"path\": \"codient_read_test.txt\"} (path relative to workspace root) and then quote or summarize the exact file contents."
	user := "Read codient_read_test.txt from the workspace and include its full text in your reply."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, want) {
		t.Fatalf("expected reply to contain file contents %q; got: %q", want, truncateRunes(reply, 1200))
	}
}

func TestIntegration_AgentListDirWorkspace(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	dir := workspaceFixture(t, map[string]string{
		"codient_listdir_marker.go": "// marker",
		"subdir/keep.txt":           "x",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	marker := "codient_listdir_marker.go"
	sys := "You have list_dir. When asked to list the workspace root, you MUST call list_dir with JSON {\"path\": \".\", \"max_depth\": 0} and then confirm whether " + marker + " appears in the listing."
	user := "List files in the workspace root (not recursive). Does " + marker + " exist here?"
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, marker) {
		t.Fatalf("expected reply to mention %q; got: %q", marker, truncateRunes(reply, 1200))
	}
}

func TestIntegration_AgentGrepWorkspace(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	needle := "GREP_UNIQUE_TOKEN_a9f2ee11"
	dir := workspaceFixture(t, map[string]string{
		"notes/grep_target.txt": "noise\n" + needle + "\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := "You have grep. To find a string in the workspace, you MUST call grep with JSON {\"pattern\": \"" + needle + "\", \"literal\": true, \"path_prefix\": \"notes\"} and then state whether the token was found."
	user := "Search the workspace for the literal string " + needle + " under the notes/ directory."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, needle) {
		t.Fatalf("expected reply to contain %q; got: %q", needle, truncateRunes(reply, 1200))
	}
}

func TestIntegration_AgentSearchFilesWorkspace(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	base := "codient_search_unique_7c4a_file"
	dir := workspaceFixture(t, map[string]string{
		base + ".txt": "content",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := "You have search_files. To find a file by name substring, you MUST call search_files with JSON {\"substring\": \"" + base + "\"} and report one matching path."
	user := "Find files whose path contains the substring " + base + "."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, base+".txt") && !strings.Contains(strings.ReplaceAll(reply, "\\", "/"), base+".txt") {
		t.Fatalf("expected reply to mention %q; got: %q", base+".txt", truncateRunes(reply, 1200))
	}
}

// Exercises run_command when the tool is enabled (tools.Default with non-nil ExecOptions).
// Requires go on PATH. Enable with CODIENT_INTEGRATION_RUN_COMMAND=1.
func TestIntegration_AgentRunCommandGoVersion(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if os.Getenv("CODIENT_INTEGRATION_RUN_COMMAND") != "1" {
		t.Skip("set CODIENT_INTEGRATION_RUN_COMMAND=1 to run run_command (requires go on PATH)")
	}
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	dir := workspaceFixture(t, map[string]string{
		"go.mod": "module codient_integration_exec_test\n\ngo 1.21\n",
	})
	exec := &tools.ExecOptions{
		Allowlist:      []string{"go"},
		TimeoutSeconds: 60,
		MaxOutputBytes: 256 * 1024,
	}
	ar, ctx, cancel := newLiveRunnerOpts(t, dir, exec)
	defer cancel()
	sys := "You have run_command. When asked to print the Go toolchain version, you MUST call run_command with JSON {\"argv\": [\"go\", \"version\"], \"cwd\": \".\"} and then summarize stdout."
	user := "Run `go version` in the workspace root and report the output."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(reply)
	if !strings.Contains(upper, "GO") || !strings.Contains(reply, "version") {
		t.Fatalf("expected reply to reflect go version output; got: %q", truncateRunes(reply, 1200))
	}
}

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("CODIENT_INTEGRATION") != "1" {
		t.Skip("set CODIENT_INTEGRATION=1 to run live API tests")
	}
}

func skipUnlessStrictTools(t *testing.T) {
	t.Helper()
	if os.Getenv("CODIENT_INTEGRATION_STRICT_TOOLS") != "1" {
		t.Skip("set CODIENT_INTEGRATION_STRICT_TOOLS=1 to enforce tool-calling (model-dependent)")
	}
}

// newLiveRunner loads config and builds the default tool registry. When workspace is non-empty,
// cfg.Workspace is set to that path (fixture tests) so it matches the tools registry root.
func newLiveRunner(t *testing.T, workspace string) (*agent.Runner, context.Context, context.CancelFunc) {
	return newLiveRunnerOpts(t, workspace, nil)
}

// newLiveRunnerOpts is like newLiveRunner but passes ExecOptions into tools.Default (non-nil enables run_command when Allowlist is set).
func newLiveRunnerOpts(t *testing.T, workspace string, exec *tools.ExecOptions) (*agent.Runner, context.Context, context.CancelFunc) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	wsRoot := ""
	if workspace != "" {
		wsRoot, err = filepath.Abs(workspace)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Workspace = wsRoot
	}
	client := openaiclient.New(cfg)
	reg := tools.Default(wsRoot, exec, nil, nil, "", nil, nil)
	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	return ar, ctx, cancel
}

// workspaceFixture creates a temp directory with the given relative path → contents map.
func workspaceFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
