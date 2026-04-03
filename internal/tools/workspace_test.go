package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbsUnderRoot(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	abs, err := absUnderRoot(dir, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(abs) != "sub" {
		t.Fatalf("got %s", abs)
	}
	_, err = absUnderRoot(dir, "../outside")
	if err == nil {
		t.Fatal("expected escape")
	}
}

func TestReadFileWorkspace_Truncation(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("a", 100)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := readFileWorkspace(dir, "big.txt", 40, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[truncated") {
		t.Fatalf("expected truncation marker: %q", out)
	}
	if len(out) > 200 {
		t.Fatalf("output unexpectedly long: %d", len(out))
	}
}

func TestReadFileWorkspace_InvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readFileWorkspace(dir, "b.bin", 1024, 0, 0)
	if err == nil {
		t.Fatal("expected utf-8 error")
	}
}

func TestListDirWorkspace_NotDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := listDirWorkspace(dir, "f.txt", 0, 10)
	if err == nil {
		t.Fatal("expected not a directory")
	}
}

func TestSearchFilesWorkspace_NeedsFilter(t *testing.T) {
	dir := t.TempDir()
	_, err := searchFilesWorkspace(dir, "", "", "", 10)
	if err == nil {
		t.Fatal("expected error without substring/suffix")
	}
}

func TestWriteFileWorkspace_InvalidMode(t *testing.T) {
	dir := t.TempDir()
	err := writeFileWorkspace(dir, "z.txt", "x", "wipe")
	if err == nil {
		t.Fatal("expected invalid mode")
	}
}
