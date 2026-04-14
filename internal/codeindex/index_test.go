package codeindex

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// fakeEmbedder returns deterministic vectors for testing.
type fakeEmbedder struct {
	dim     int
	callLog [][]string
}

func (f *fakeEmbedder) CreateEmbedding(_ context.Context, model string, inputs []string) ([][]float64, error) {
	f.callLog = append(f.callLog, inputs)
	vecs := make([][]float64, len(inputs))
	for i, s := range inputs {
		v := make([]float64, f.dim)
		// Simple hash-based vector: spread characters across dimensions.
		for j, c := range s {
			v[(j*7)%f.dim] += float64(c) / 1000.0
		}
		norm := 0.0
		for _, x := range v {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for k := range v {
				v[k] /= norm
			}
		}
		vecs[i] = v
	}
	return vecs, nil
}

func setupTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "auth.go", "package main\n\nfunc Authenticate(user, pass string) bool { return true }\n")
	writeFile(t, dir, "db.go", "package main\n\nfunc ConnectDB(dsn string) error { return nil }\n")
	writeFile(t, dir, "README.md", "# Test Project\n\nA test project for indexing.\n")

	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAndQuery(t *testing.T) {
	dir := setupTestWorkspace(t)
	emb := &fakeEmbedder{dim: 32}
	idx := New(dir, emb, "test-model")

	ctx := context.Background()
	idx.BuildOrUpdate(ctx)

	if idx.BuildErr() != nil {
		t.Fatalf("build error: %v", idx.BuildErr())
	}
	if idx.Len() == 0 {
		t.Fatal("expected indexed files")
	}

	results, err := idx.Query(ctx, "authentication login", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	for _, r := range results {
		if r.Path == "" {
			t.Fatal("empty path in result")
		}
		if r.Score < -1 || r.Score > 1 {
			t.Fatalf("score out of range: %f", r.Score)
		}
	}
}

func TestIncrementalUpdate(t *testing.T) {
	dir := setupTestWorkspace(t)
	emb := &fakeEmbedder{dim: 32}
	idx := New(dir, emb, "test-model")

	ctx := context.Background()
	idx.BuildOrUpdate(ctx)
	firstLen := idx.Len()
	firstCalls := len(emb.callLog)

	// Build again without changes — should reuse cached vectors.
	idx2 := New(dir, emb, "test-model")
	idx2.BuildOrUpdate(ctx)
	if idx2.Len() != firstLen {
		t.Fatalf("expected same file count %d, got %d", firstLen, idx2.Len())
	}
	if len(emb.callLog) != firstCalls {
		t.Fatalf("expected no new embedding calls, got %d", len(emb.callLog)-firstCalls)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	model := "test-model"
	entries := []Entry{
		{Path: "a.go", Vector: []float64{0.1, 0.2, 0.3}},
		{Path: "b.go", Vector: []float64{0.4, 0.5, 0.6}},
	}
	if err := saveIndex(dir, model, entries); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadIndex(dir, model)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(entries) {
		t.Fatalf("expected %d entries, got %d", len(entries), len(loaded))
	}
	for i, e := range loaded {
		if e.Path != entries[i].Path {
			t.Fatalf("path mismatch: %s != %s", e.Path, entries[i].Path)
		}
		if len(e.Vector) != len(entries[i].Vector) {
			t.Fatalf("vector length mismatch")
		}
		for j := range e.Vector {
			if math.Abs(e.Vector[j]-entries[i].Vector[j]) > 1e-9 {
				t.Fatalf("vector value mismatch at %d: %f != %f", j, e.Vector[j], entries[i].Vector[j])
			}
		}
	}
}

func TestStoreModelMismatch(t *testing.T) {
	dir := t.TempDir()
	entries := []Entry{{Path: "a.go", Vector: []float64{0.1}}}
	if err := saveIndex(dir, "model-a", entries); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadIndex(dir, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != nil {
		t.Fatal("expected nil for model mismatch")
	}
}

func TestCosineSimilarity(t *testing.T) {
	a := []float64{1, 0, 0}
	b := []float64{1, 0, 0}
	if s := cosineSimilarity(a, b); math.Abs(s-1.0) > 1e-9 {
		t.Fatalf("identical vectors should have similarity 1.0, got %f", s)
	}

	c := []float64{0, 1, 0}
	if s := cosineSimilarity(a, c); math.Abs(s) > 1e-9 {
		t.Fatalf("orthogonal vectors should have similarity 0.0, got %f", s)
	}

	d := []float64{-1, 0, 0}
	if s := cosineSimilarity(a, d); math.Abs(s+1.0) > 1e-9 {
		t.Fatalf("opposite vectors should have similarity -1.0, got %f", s)
	}
}

func TestWalker_SkipsBinary(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "text.go", "package main\n")
	if err := os.WriteFile(filepath.Join(dir, "binary.dat"), []byte{0, 1, 2, 0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}

	docs, err := walkWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Path == "binary.dat" {
			t.Fatal("binary file should be skipped")
		}
	}
}

func TestWalker_SkipsExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "code.go", "package main\n")
	writeFile(t, dir, "image.png", "not really a png")
	writeFile(t, dir, "archive.zip", "not really a zip")

	docs, err := walkWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Path == "image.png" || d.Path == "archive.zip" {
			t.Fatalf("file %s should be skipped by extension", d.Path)
		}
	}
}
