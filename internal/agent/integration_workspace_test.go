//go:build integration

package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codient/internal/agent"
	"codient/internal/config"
	"codient/internal/openaiclient"
	"codient/internal/stringutil"
	"codient/internal/tools"
)

// ============================================================================
// Mutating workspace tools: write_file, str_replace, patch_file, insert_lines
// ============================================================================

func TestIntegration_AgentWriteFile(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, nil)
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	const marker = "WRITE_FILE_CONTENT_c3a7b1e0"
	sys := "You have write_file. When asked to create a file, you MUST call write_file with JSON {\"path\": \"output.txt\", \"content\": \"" + marker + "\\n\"} and then confirm the file was created."
	user := "Create a file named output.txt with the content: " + marker
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil {
		t.Fatalf("write_file did not create the file: %v", err)
	}
	if !strings.Contains(string(data), marker) {
		t.Fatalf("file content = %q, want %q", string(data), marker)
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentStrReplace(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"target.txt": "Hello OLD_TOKEN_f9e2 world\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have str_replace. To replace text in a file, call str_replace with JSON {"path": "target.txt", "old_string": "OLD_TOKEN_f9e2", "new_string": "NEW_TOKEN_a1b2"}. Then confirm the replacement.`
	user := "In target.txt, replace OLD_TOKEN_f9e2 with NEW_TOKEN_a1b2."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "NEW_TOKEN_a1b2") {
		t.Fatalf("str_replace did not apply; file = %q", content)
	}
	if strings.Contains(content, "OLD_TOKEN_f9e2") {
		t.Fatalf("old token still present; file = %q", content)
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentPatchFile(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"patch_me.txt": "line1\nPATCH_OLD_aa11\nline3\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have patch_file. Apply a unified diff to patch_me.txt that replaces the line "PATCH_OLD_aa11" with "PATCH_NEW_bb22". ` +
		`Call patch_file with JSON {"path": "patch_me.txt", "diff": "--- a/patch_me.txt\n+++ b/patch_me.txt\n@@ -1,3 +1,3 @@\n line1\n-PATCH_OLD_aa11\n+PATCH_NEW_bb22\n line3\n"}. Then confirm.`
	user := "Patch patch_me.txt: replace the line PATCH_OLD_aa11 with PATCH_NEW_bb22 using a unified diff."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "patch_me.txt"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "PATCH_NEW_bb22") {
		t.Fatalf("patch not applied; file = %q", content)
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentInsertLines(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"insert_target.txt": "line1\nline2\nline3\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have insert_lines. To insert text after line 2 of insert_target.txt, call insert_lines with JSON {"path": "insert_target.txt", "after_line": 2, "content": "INSERTED_LINE_cc33\n"}. Then confirm.`
	user := "Insert the line INSERTED_LINE_cc33 after line 2 of insert_target.txt."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "insert_target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "INSERTED_LINE_cc33") {
		t.Fatalf("insert_lines did not insert; file = %q", string(data))
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

// ============================================================================
// Filesystem management tools: glob_files, ensure_dir, remove_path,
// move_path, copy_path, path_stat
// ============================================================================

func TestIntegration_AgentGlobFiles(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"src/alpha.go":  "package alpha\n",
		"src/beta.go":   "package beta\n",
		"docs/readme.md": "# Docs\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have glob_files. To find .go files, call glob_files with JSON {"pattern": "**/*.go"}. Then list the matching paths in your reply.`
	user := "Find all .go files in the workspace using glob_files."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(reply)
	if !strings.Contains(lower, "alpha.go") || !strings.Contains(lower, "beta.go") {
		t.Fatalf("glob_files should find both .go files; reply: %q", stringutil.TruncateRunes(reply, 1200))
	}
	if strings.Contains(lower, "readme.md") {
		t.Logf("warning: glob_files returned readme.md (unexpected for *.go pattern)")
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentEnsureDir(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, nil)
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have ensure_dir. To create a directory, call ensure_dir with JSON {"path": "new_dir/sub"}. Then confirm it was created.`
	user := "Create the directory new_dir/sub using ensure_dir."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "new_dir", "sub"))
	if err != nil {
		t.Fatalf("ensure_dir did not create directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected a directory")
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentPathStat(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"stat_target.txt": "some content for stat\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have path_stat. To get info about a file, call path_stat with JSON {"path": "stat_target.txt"}. Report the file type and size.`
	user := "Use path_stat on stat_target.txt and tell me its type and size."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(reply)
	if !strings.Contains(lower, "file") {
		t.Fatalf("expected reply to mention 'file'; got: %q", stringutil.TruncateRunes(reply, 800))
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentMovePath(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"move_src.txt": "MOVE_CONTENT_dd44\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have move_path. To rename a file, call move_path with JSON {"from": "move_src.txt", "to": "move_dst.txt"}. Then confirm.`
	user := "Rename move_src.txt to move_dst.txt using move_path."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "move_src.txt")); !os.IsNotExist(err) {
		t.Fatal("source file should no longer exist after move")
	}
	data, err := os.ReadFile(filepath.Join(dir, "move_dst.txt"))
	if err != nil {
		t.Fatalf("destination file not found: %v", err)
	}
	if !strings.Contains(string(data), "MOVE_CONTENT_dd44") {
		t.Fatalf("destination content wrong: %q", string(data))
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentCopyPath(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"copy_src.txt": "COPY_CONTENT_ee55\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have copy_path. To copy a file, call copy_path with JSON {"from": "copy_src.txt", "to": "copy_dst.txt"}. Then confirm both files exist.`
	user := "Copy copy_src.txt to copy_dst.txt using copy_path."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	srcData, err := os.ReadFile(filepath.Join(dir, "copy_src.txt"))
	if err != nil {
		t.Fatalf("source should still exist: %v", err)
	}
	dstData, err := os.ReadFile(filepath.Join(dir, "copy_dst.txt"))
	if err != nil {
		t.Fatalf("destination not found: %v", err)
	}
	if string(srcData) != string(dstData) {
		t.Fatalf("content mismatch: src=%q dst=%q", srcData, dstData)
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

func TestIntegration_AgentRemovePath(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"to_delete.txt": "delete me\n",
	})
	ar, ctx, cancel := newLiveRunner(t, dir)
	defer cancel()
	sys := `You have remove_path. To delete a file, call remove_path with JSON {"path": "to_delete.txt"}. Then confirm it was removed.`
	user := "Delete to_delete.txt using remove_path."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "to_delete.txt")); !os.IsNotExist(err) {
		t.Fatal("file should have been deleted")
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

// ============================================================================
// run_shell
// ============================================================================

func TestIntegration_AgentRunShell(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	if os.Getenv("CODIENT_INTEGRATION_RUN_COMMAND") != "1" {
		t.Skip("set CODIENT_INTEGRATION_RUN_COMMAND=1 to run shell tests")
	}
	dir := workspaceFixture(t, map[string]string{
		"go.mod": "module codient_shell_test\n\ngo 1.21\n",
	})
	exec := &tools.ExecOptions{
		Allowlist:      []string{"go", "sh", "cmd"},
		TimeoutSeconds: 60,
		MaxOutputBytes: 256 * 1024,
	}
	ar, ctx, cancel := newLiveRunnerOpts(t, dir, exec)
	defer cancel()
	sys := `You have run_shell. To run a shell command, call run_shell with JSON {"command": "go version", "cwd": "."}. Then report the output.`
	user := "Run 'go version' using run_shell and tell me the output."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(reply)
	if !strings.Contains(upper, "GO") {
		t.Fatalf("expected reply to mention Go; got: %q", stringutil.TruncateRunes(reply, 1200))
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

// ============================================================================
// fetch_url
// ============================================================================

func TestIntegration_AgentFetchURL(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, nil)
	ar, ctx, cancel := newLiveRunnerWithFetch(t, dir)
	defer cancel()
	sys := `You have fetch_url. To fetch a web page, call fetch_url with JSON {"url": "https://example.com"}. Then summarize the HTTP status and page title.`
	user := "Fetch https://example.com using fetch_url and tell me the HTTP status and page title."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(reply)
	if !strings.Contains(lower, "example") {
		t.Fatalf("expected reply to mention 'example'; got: %q", stringutil.TruncateRunes(reply, 1200))
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

// ============================================================================
// Multi-turn conversation (RunConversation with history)
// ============================================================================

func TestIntegration_AgentMultiTurn(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	sys := "You are a helpful assistant. Remember what the user tells you across messages."

	// Turn 1: establish a fact
	const secret = "MULTI_TURN_SECRET_7x9q"
	reply1, history, _, err := ar.RunConversation(ctx, sys, nil, "Remember this code: "+secret+". Just say OK.", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("turn 1: %s", stringutil.TruncateRunes(reply1, 300))
	if len(history) == 0 {
		t.Fatal("expected non-empty history after turn 1")
	}

	// Turn 2: recall the fact
	reply2, _, _, err := ar.RunConversation(ctx, sys, history, "What was the code I asked you to remember? Reply with just the code.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply2, secret) {
		t.Fatalf("model should recall %q; turn 2 reply: %q", secret, stringutil.TruncateRunes(reply2, 800))
	}
	t.Logf("turn 2: %s", stringutil.TruncateRunes(reply2, 300))
}

// ============================================================================
// AutoCheck callback fires after mutating tool
// ============================================================================

func TestIntegration_AgentAutoCheck(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, nil)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	wsRoot, _ := filepath.Abs(dir)
	cfg.Workspace = wsRoot
	client := openaiclient.New(cfg)
	reg := tools.Default(wsRoot, nil, nil, nil, "", nil, nil)

	autoCheckCalled := false
	ar := &agent.Runner{
		LLM:   client,
		Cfg:   cfg,
		Tools: reg,
		AutoCheck: func(ctx context.Context) agent.AutoCheckOutcome {
			autoCheckCalled = true
			return agent.AutoCheckOutcome{Progress: "auto-check: ok"}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	sys := `You have write_file. When asked to create a file, call write_file with JSON {"path": "autocheck_test.txt", "content": "hello\n"} and then confirm.`
	user := "Create autocheck_test.txt with content 'hello'."
	_, _, err = ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !autoCheckCalled {
		t.Fatal("AutoCheck should have been called after write_file")
	}
}

// ============================================================================
// PostReplyCheck callback injects follow-up
// ============================================================================

func TestIntegration_AgentPostReplyCheck(t *testing.T) {
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

	checkFired := false
	ar := &agent.Runner{
		LLM:   client,
		Cfg:   cfg,
		Tools: reg,
		PostReplyCheck: func(ctx context.Context, info agent.PostReplyCheckInfo) string {
			if checkFired {
				return ""
			}
			checkFired = true
			return "Now say exactly: POST_REPLY_CONFIRMED_88zz"
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	reply, _, err := ar.Run(ctx, "You are a helpful assistant.", "Say hello.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !checkFired {
		t.Fatal("PostReplyCheck should have fired")
	}
	if !strings.Contains(reply, "POST_REPLY_CONFIRMED_88zz") {
		t.Logf("model may not have obeyed the injected instruction (model-dependent); reply: %q", stringutil.TruncateRunes(reply, 800))
	}
}

// ============================================================================
// Read-only mode (ask) — verify no write tools are available
// ============================================================================

func TestIntegration_AgentReadOnlyMode(t *testing.T) {
	skipUnlessIntegration(t)
	skipUnlessStrictTools(t)
	dir := workspaceFixture(t, map[string]string{
		"readonly_file.txt": "READONLY_CONTENT_ff66\n",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	wsRoot, _ := filepath.Abs(dir)
	cfg.Workspace = wsRoot
	client := openaiclient.New(cfg)
	reg := tools.DefaultReadOnly(wsRoot, nil, nil, "", nil)

	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	sys := "You have read_file. Read readonly_file.txt and quote its content. You do NOT have write_file."
	user := "Read readonly_file.txt and tell me its contents."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "READONLY_CONTENT_ff66") {
		t.Fatalf("expected file content in reply; got: %q", stringutil.TruncateRunes(reply, 1200))
	}

	if registryHas(reg, "write_file") {
		t.Fatal("read-only registry should NOT have write_file")
	}
	if registryHas(reg, "str_replace") {
		t.Fatal("read-only registry should NOT have str_replace")
	}
	t.Logf("reply: %s", stringutil.TruncateRunes(reply, 500))
}

// ============================================================================
// Plan mode — verify echo is not available (prevents model from using echo as shortcut)
// ============================================================================

func TestIntegration_AgentPlanModeNoEcho(t *testing.T) {
	skipUnlessIntegration(t)
	dir := workspaceFixture(t, map[string]string{
		"plan_file.txt": "PLAN_READ_CONTENT_gg77\n",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	wsRoot, _ := filepath.Abs(dir)
	cfg.Workspace = wsRoot
	client := openaiclient.New(cfg)
	reg := tools.DefaultReadOnlyPlan(wsRoot, nil, nil, "", nil)

	if registryHas(reg, "echo") {
		t.Fatal("plan mode registry should NOT have echo")
	}
	if registryHas(reg, "write_file") {
		t.Fatal("plan mode registry should NOT have write_file")
	}
	if !registryHas(reg, "read_file") {
		t.Fatal("plan mode registry SHOULD have read_file")
	}

	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	sys := "You are a planning assistant. You have read_file and list_dir. Analyze the workspace and outline a plan."
	user := "What files are in the workspace? Read plan_file.txt and summarize what you find."
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("plan mode reply: %s", stringutil.TruncateRunes(reply, 500))
}

// ============================================================================
// Helpers
// ============================================================================

func registryHas(reg *tools.Registry, name string) bool {
	for _, n := range reg.Names() {
		if n == name {
			return true
		}
	}
	return false
}

func newLiveRunnerWithFetch(t *testing.T, workspace string) (*agent.Runner, context.Context, context.CancelFunc) {
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
	fetch := &tools.FetchOptions{
		AllowHosts:         []string{"example.com"},
		IncludePreapproved: false,
		TimeoutSec:         30,
	}
	reg := tools.Default(wsRoot, nil, fetch, nil, "", nil, nil)
	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	return ar, ctx, cancel
}
