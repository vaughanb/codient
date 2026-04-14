package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveMoveCopyWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("package sub"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyPathWorkspace(dir, "sub", "sub2"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "sub2", "b.go"))
	if err != nil || string(b) != "package sub" {
		t.Fatalf("copy dir: %v %q", err, b)
	}

	if err := movePathWorkspace(dir, "a.txt", "moved.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "moved.txt")); err != nil {
		t.Fatal(err)
	}

	if err := removePathWorkspace(dir, "sub2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub2")); err == nil {
		t.Fatal("expected sub2 removed")
	}
}

func TestPathStatWorkspace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.go")
	if err := os.WriteFile(p, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := pathStatWorkspace(dir, "f.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "exists: true") || !strings.Contains(s, "kind: file") {
		t.Fatalf("got %q", s)
	}
	s2, err := pathStatWorkspace(dir, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s2, "exists: false") {
		t.Fatalf("got %q", s2)
	}
}

func TestGlobFilesWorkspace(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "pkg"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "a_test.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("y"), 0o644)

	out, err := globFilesWorkspace(dir, ".", "*_test.go", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg/a_test.go") || strings.Contains(out, "a.go") {
		t.Fatalf("basename glob: %q", out)
	}

	out2, err := globFilesWorkspace(dir, "pkg", "a.go", 50)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out2) != "a.go" {
		t.Fatalf("full path glob: %q", out2)
	}
}

func TestInsertLinesWorkspace(t *testing.T) {
	t.Run("append to end (default)", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "f.go")
		os.WriteFile(f, []byte("line1\nline2\n"), 0o644)

		out, err := insertLinesWorkspace(dir, "f.go", "line3\nline4\n", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "inserted") {
			t.Fatalf("unexpected output: %q", out)
		}
		got, _ := os.ReadFile(f)
		if string(got) != "line1\nline2\nline3\nline4\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("append to file without trailing newline", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "f.go")
		os.WriteFile(f, []byte("line1\nline2"), 0o644)

		_, err := insertLinesWorkspace(dir, "f.go", "line3\n", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(f)
		if string(got) != "line1\nline2\nline3\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("prepend to beginning", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "f.go")
		os.WriteFile(f, []byte("line2\nline3\n"), 0o644)

		_, err := insertLinesWorkspace(dir, "f.go", "line1\n", "beginning", 0)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(f)
		if string(got) != "line1\nline2\nline3\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("insert after specific line", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "f.go")
		os.WriteFile(f, []byte("aaa\nbbb\nccc\n"), 0o644)

		_, err := insertLinesWorkspace(dir, "f.go", "INSERTED\n", "", 2)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(f)
		if string(got) != "aaa\nbbb\nINSERTED\nccc\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("after_line beyond file length appends", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "f.go")
		os.WriteFile(f, []byte("only\n"), 0o644)

		_, err := insertLinesWorkspace(dir, "f.go", "appended\n", "", 999)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(f)
		if string(got) != "only\nappended\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("empty file append", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "f.go")
		os.WriteFile(f, []byte(""), 0o644)

		_, err := insertLinesWorkspace(dir, "f.go", "first\n", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(f)
		if string(got) != "first\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("via registry", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644)
		r := Default(dir, nil, nil, nil, "", nil)
		raw, _ := json.Marshal(map[string]any{"path": "a.txt", "content": "world\n"})
		out, err := r.Run(context.Background(), "insert_lines", json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "inserted") {
			t.Fatalf("got %q", out)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
		if string(got) != "hello\nworld\n" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestMutatingToolsViaRegistry(t *testing.T) {
	dir := t.TempDir()
	r := Default(dir, nil, nil, nil, "", nil)
	_, err := r.Run(context.Background(), "write_file", json.RawMessage(`{"path":"t.txt","content":"z","mode":"create"}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), "copy_path", json.RawMessage(`{"from":"t.txt","to":"u.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "copied") {
		t.Fatalf("got %q", out)
	}
	_, err = r.Run(context.Background(), "remove_path", json.RawMessage(`{"path":"u.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
}
