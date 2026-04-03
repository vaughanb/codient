package planstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskSlug(t *testing.T) {
	if g := TaskSlug("Build a TODO CLI", "", ""); g != "build-a-todo-cli" {
		t.Fatalf("goal: got %q want build-a-todo-cli", g)
	}
	if g := TaskSlug("", "/tmp/My Task Spec.md", ""); g != "my-task-spec" {
		t.Fatalf("task file: %q", g)
	}
	if g := TaskSlug("", "", "  First line here\nrest"); g != "first-line-here" {
		t.Fatalf("user line: %q", g)
	}
	if g := TaskSlug("", "", ""); g != "plan" {
		t.Fatalf("fallback: %q", g)
	}
}

func TestLooksLikeReadyToImplement(t *testing.T) {
	if !LooksLikeReadyToImplement("## Ready to implement\n\ngo run") {
		t.Fatal("expected true")
	}
	if LooksLikeReadyToImplement("still thinking") {
		t.Fatal("expected false")
	}
}

func TestSave_CreatesFile(t *testing.T) {
	tmp := t.TempDir()
	ts := time.Date(2026, 4, 3, 14, 30, 22, 123456789, time.UTC)
	path, err := Save(tmp, "", "my-task", "# Plan\n", ts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(path), "my-task_20260403-143022_") || !strings.HasSuffix(path, ".md") {
		t.Fatalf("unexpected name: %s", filepath.Base(path))
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "# Plan\n" {
		t.Fatalf("content: %v %q", err, b)
	}
}

func TestDir_DefaultUnderWorkspace(t *testing.T) {
	tmp := t.TempDir()
	d, err := Dir(tmp, "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, ".codient", "plans")
	if filepath.Clean(d) != filepath.Clean(want) {
		t.Fatalf("got %q want %q", d, want)
	}
}
