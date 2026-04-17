package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_mergesUserAndWorkspace(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODIENT_STATE_DIR", state)

	userFile := filepath.Join(state, "hooks.json")
	if err := os.WriteFile(userFile, []byte(`{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "echo",
        "hooks": [{ "type": "command", "command": "true" }]
      }
    ]
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(ws, ".codient"), 0o755); err != nil {
		t.Fatal(err)
	}
	wsFile := filepath.Join(ws, ".codient", "hooks.json")
	if err := os.WriteFile(wsFile, []byte(`{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "get_time",
        "hooks": [{ "type": "command", "command": "true" }]
      }
    ]
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Paths) != 2 {
		t.Fatalf("paths: got %v want 2 files", loaded.Paths)
	}
	groups := loaded.ByEvent[EventPreToolUse]
	if len(groups) != 2 {
		t.Fatalf("PreToolUse groups: got %d want 2", len(groups))
	}
}

func TestMatcherMatches(t *testing.T) {
	t.Parallel()
	if !matcherMatches("", "anything") {
		t.Fatal("empty matcher should match")
	}
	if !matcherMatches("echo|get_time", "echo") {
		t.Fatal("regex alt")
	}
	if matcherMatches("echo", "grep") {
		t.Fatal("should not match")
	}
}
