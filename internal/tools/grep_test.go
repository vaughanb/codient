package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepStdlib_FindsMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world\nneedle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := grepWorkspace(context.Background(), root, "", "needle", true, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "needle") {
		t.Fatalf("expected needle in %q", out)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("expected path in %q", out)
	}
}

func TestGrepStdlib_GlobFilters(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("foo\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo\n"), 0o644)
	out, err := grepWorkspace(context.Background(), root, "", "foo", true, "*.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("expected .go only: %q", out)
	}
	if strings.Contains(out, "a.txt") {
		t.Fatalf("should not match .txt: %q", out)
	}
}

func TestGrepStdlib_NoMatches(t *testing.T) {
	root := t.TempDir()
	out, err := grepWorkspace(context.Background(), root, "", "nope", true, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no matches)" {
		t.Fatalf("got %q", out)
	}
}

func TestGrepStdlib_InvalidRegex(t *testing.T) {
	root := t.TempDir()
	_, err := grepWorkspace(context.Background(), root, "", "(", false, "", 10)
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected invalid regex error, got %v", err)
	}
}
