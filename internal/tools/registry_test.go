package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEcho(t *testing.T) {
	r := Default("", nil)
	out, err := r.Run(context.Background(), "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "hi" {
		t.Fatalf("got %q", out)
	}
}

func TestReadFileWorkspace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("line1\nline2\nline3"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := readFileWorkspace(dir, "a.txt", 1024, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s != "line1\nline2\nline3" {
		t.Fatalf("got %q", s)
	}
	s2, err := readFileWorkspace(dir, "a.txt", 1024, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s2, "line2") || strings.Contains(s2, "line3") {
		t.Fatalf("line slice: %q", s2)
	}
	_, err = readFileWorkspace(dir, "../outside", 100, 0, 0)
	if err == nil {
		t.Fatal("expected escape error")
	}
}

func TestListDirWorkspace(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "root.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "sub", "a.go"), []byte("y"), 0o644)
	out, err := listDirWorkspace(dir, ".", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "root.go") || !strings.Contains(out, "sub/") {
		t.Fatalf("flat list: %q", out)
	}
	if strings.Contains(out, "a.go") {
		t.Fatalf("should not recurse at depth 0: %q", out)
	}
	out2, err := listDirWorkspace(dir, ".", 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "sub/a.go") {
		t.Fatalf("deep list: %q", out2)
	}
}

func TestSearchFilesWorkspace(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("x"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "d"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "d", "bar.txt"), []byte("y"), 0o644)
	out, err := searchFilesWorkspace(dir, "", "", ".go", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "foo.go") || strings.Contains(out, "bar.txt") {
		t.Fatalf("suffix: %q", out)
	}
	out2, err := searchFilesWorkspace(dir, "", "bar", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "d/bar.txt") {
		t.Fatalf("substring: %q", out2)
	}
}

func TestWriteFileWorkspace(t *testing.T) {
	dir := t.TempDir()
	err := writeFileWorkspace(dir, "x/y.txt", "hello", "create")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "x", "y.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("read back: %v %q", err, b)
	}
	if writeFileWorkspace(dir, "x/y.txt", "nope", "create") == nil {
		t.Fatal("create should fail when exists")
	}
	if err := writeFileWorkspace(dir, "x/y.txt", "bye", "overwrite"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "x", "y.txt"))
	if string(b) != "bye" {
		t.Fatalf("overwrite: %q", b)
	}
}

func TestWriteFileToolViaRegistry(t *testing.T) {
	dir := t.TempDir()
	r := Default(dir, nil)
	out, err := r.Run(context.Background(), "write_file", json.RawMessage(`{
		"path": "pkg/x.go",
		"content": "package pkg\n",
		"mode": "create"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote") {
		t.Fatalf("got %q", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "pkg", "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "package pkg\n" {
		t.Fatalf("content: %q", b)
	}
}

func TestDefaultWorkspaceToolsRegistered(t *testing.T) {
	dir := t.TempDir()
	r := Default(dir, nil)
	names := map[string]bool{}
	for _, n := range r.Names() {
		names[n] = true
	}
	for _, want := range []string{"read_file", "list_dir", "search_files", "write_file"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

func TestDefaultReadOnly_OmitsMutatingTools(t *testing.T) {
	dir := t.TempDir()
	r := DefaultReadOnly(dir)
	names := r.Names()
	for _, n := range names {
		if n == "write_file" || n == "run_command" {
			t.Fatalf("unexpected tool %q in read-only registry", n)
		}
	}
	found := false
	for _, n := range names {
		if n == "read_file" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected read_file, got %v", names)
	}
}

func TestDefaultReadOnlyPlan_NoEcho(t *testing.T) {
	dir := t.TempDir()
	r := DefaultReadOnlyPlan(dir)
	for _, n := range r.Names() {
		if n == "echo" {
			t.Fatal("plan registry must not include echo")
		}
	}
	hasTime := false
	for _, n := range r.Names() {
		if n == "get_time" {
			hasTime = true
		}
	}
	if !hasTime {
		t.Fatalf("expected get_time in plan registry: %v", r.Names())
	}
}

func TestDefault_WithAllowlist_IncludesRunCommand(t *testing.T) {
	dir := t.TempDir()
	r := Default(dir, &ExecOptions{
		Allowlist:      []string{"go"},
		TimeoutSeconds: 30,
		MaxOutputBytes: 1024,
	})
	has := false
	for _, n := range r.Names() {
		if n == "run_command" {
			has = true
		}
	}
	if !has {
		t.Fatal("expected run_command when allowlist is set")
	}
}
