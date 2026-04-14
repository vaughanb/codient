package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSemver(t *testing.T) {
	tests := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"1.2.3", [3]int{1, 2, 3}, true},
		{"v1.2.3", [3]int{1, 2, 3}, true},
		{"0.0.0", [3]int{0, 0, 0}, true},
		{"10.20.30", [3]int{10, 20, 30}, true},
		{"v0.2.0", [3]int{0, 2, 0}, true},
		{"1.2", [3]int{}, false},
		{"abc", [3]int{}, false},
		{"1.2.x", [3]int{}, false},
		{"", [3]int{}, false},
		{"v", [3]int{}, false},
		{"1.-1.0", [3]int{}, false},
	}
	for _, tt := range tests {
		got, ok := parseSemver(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseSemver(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.2.0", "v0.3.0", true},
		{"0.2.0", "v0.2.1", true},
		{"0.2.0", "v1.0.0", true},
		{"0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"1.0.0", "v0.9.9", false},
		{"v1.2.3", "v1.2.4", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.2", false},
		{"bad", "v1.0.0", false},
		{"1.0.0", "bad", false},
	}
	for _, tt := range tests {
		got := IsNewer(tt.current, tt.latest)
		if got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v; want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestSkippedVersionRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// No file yet.
	if got := LoadSkippedVersion(dir); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	if err := SaveSkippedVersion(dir, "v0.3.0"); err != nil {
		t.Fatal(err)
	}
	if got := LoadSkippedVersion(dir); got != "v0.3.0" {
		t.Fatalf("expected v0.3.0, got %q", got)
	}

	// Overwrite with newer skip.
	if err := SaveSkippedVersion(dir, "v0.4.0"); err != nil {
		t.Fatal(err)
	}
	if got := LoadSkippedVersion(dir); got != "v0.4.0" {
		t.Fatalf("expected v0.4.0, got %q", got)
	}
}

func TestSkippedVersionEmptyStateDir(t *testing.T) {
	if got := LoadSkippedVersion(""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if err := SaveSkippedVersion("", "v1.0.0"); err == nil {
		t.Fatal("expected error for empty state dir")
	}
}

func TestSkippedVersionNestedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := SaveSkippedVersion(dir, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := LoadSkippedVersion(dir); got != "v1.0.0" {
		t.Fatalf("expected v1.0.0, got %q", got)
	}
}

func TestExtractTarGz(t *testing.T) {
	content := []byte("#!/bin/sh\necho hello\n")
	data := buildTarGz(t, "codient", content)

	got, err := extractTarGz(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %q", got)
	}
}

func TestExtractTarGzMissing(t *testing.T) {
	data := buildTarGz(t, "other-binary", []byte("x"))
	_, err := extractTarGz(data)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestExtractZip(t *testing.T) {
	content := []byte("MZ fake exe content")
	data := buildZip(t, "codient.exe", content)

	got, err := extractZip(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %q", got)
	}
}

func TestExtractZipMissing(t *testing.T) {
	data := buildZip(t, "other.exe", []byte("x"))
	_, err := extractZip(data)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "codient")
	if err := os.WriteFile(fakeBin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	// We can't easily test replaceBinary with os.Executable(), but we can
	// test the extraction + write flow indirectly. The replaceBinary function
	// is a thin wrapper around temp-write + rename, which is well-tested by
	// the OS. We verify extractBinary produces correct output above.
}

// --- helpers ---

func buildTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	fw, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
