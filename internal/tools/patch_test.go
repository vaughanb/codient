package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUnifiedDiff_SingleHunk(t *testing.T) {
	diff := `@@ -2,3 +2,3 @@
 ctx1
-old
+new
 ctx2
`
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("len=%d", len(hunks))
	}
	h := hunks[0]
	if h.OldStart != 2 || h.OldCount != 3 || h.NewStart != 2 || h.NewCount != 3 {
		t.Fatalf("header: %+v", h)
	}
	if len(h.Lines) != 4 {
		t.Fatalf("lines %d", len(h.Lines))
	}
}

func TestParseUnifiedDiff_MultipleHunks(t *testing.T) {
	diff := `@@ -1,2 +1,2 @@
 a
-b
+b2
@@ -4,2 +4,2 @@
 x
-y
+y2
`
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 2 {
		t.Fatalf("len=%d", len(hunks))
	}
	if hunks[0].OldStart != 1 || hunks[1].OldStart != 4 {
		t.Fatalf("starts: %+v %+v", hunks[0], hunks[1])
	}
}

func TestParseUnifiedDiff_SkipsHeaders(t *testing.T) {
	diff := `diff --git a/f b/f
--- a/f
+++ b/f
@@ -1,2 +1,2 @@
 line1
-old
+new
`
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("len=%d", len(hunks))
	}
}

func TestParseUnifiedDiff_Malformed(t *testing.T) {
	_, err := parseUnifiedDiff("not a diff")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyHunks_AddLines(t *testing.T) {
	original := "a\nb\nc\n"
	hunks := []diffHunk{{
		OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 2,
		Lines: []diffLine{
			{Op: ' ', Text: "a"},
			{Op: '+', Text: "insert"},
			{Op: ' ', Text: "b"},
		},
	}}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a\ninsert\nb\nc\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyHunks_RemoveLines(t *testing.T) {
	original := "a\nb\nc\n"
	hunks := []diffHunk{{
		OldStart: 2, OldCount: 1, NewStart: 2, NewCount: 0,
		Lines: []diffLine{
			{Op: '-', Text: "b"},
		},
	}}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a\nc\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyHunks_ReplaceLines(t *testing.T) {
	original := "a\nb\nc\n"
	hunks := []diffHunk{{
		OldStart: 2, OldCount: 1, NewStart: 2, NewCount: 1,
		Lines: []diffLine{
			{Op: '-', Text: "b"},
			{Op: '+', Text: "beta"},
		},
	}}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a\nbeta\nc\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyHunks_MultiHunk(t *testing.T) {
	original := "one\ntwo\nthree\nfour\n"
	hunks := []diffHunk{
		{
			OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
			Lines: []diffLine{
				{Op: '-', Text: "one"},
				{Op: '+', Text: "1"},
			},
		},
		{
			OldStart: 4, OldCount: 1, NewStart: 4, NewCount: 1,
			Lines: []diffLine{
				{Op: '-', Text: "four"},
				{Op: '+', Text: "IV"},
			},
		},
	}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if out != "1\ntwo\nthree\nIV\n" {
		t.Fatalf("got %q", out)
	}
}

func TestApplyHunks_FuzzMatch(t *testing.T) {
	original := "a\nb\nc\nd\ne\n"
	// Real match starts at line 3 (c); header says line 5 (wrong by 2)
	hunks := []diffHunk{{
		OldStart: 5, OldCount: 1, NewStart: 5, NewCount: 1,
		Lines: []diffLine{
			{Op: '-', Text: "c"},
			{Op: '+', Text: "C"},
		},
	}}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a\nb\nC\nd\ne\n" {
		t.Fatalf("fuzz replace failed: %q", out)
	}
}

func TestApplyHunks_ContextMismatch(t *testing.T) {
	original := "a\nb\nc\n"
	hunks := []diffHunk{{
		OldStart: 2, OldCount: 1, NewStart: 2, NewCount: 1,
		Lines: []diffLine{
			{Op: '-', Text: "wrong"},
			{Op: '+', Text: "x"},
		},
	}}
	_, err := applyHunks(original, hunks)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyHunks_PreservesTrailingNewline(t *testing.T) {
	original := "a\nb\n"
	hunks := []diffHunk{{
		OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
		Lines: []diffLine{
			{Op: '-', Text: "a"},
			{Op: '+', Text: "A"},
		},
	}}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("want trailing newline, got %q", out)
	}
}

func TestApplyHunks_NoTrailingNewline(t *testing.T) {
	original := "a\nb"
	hunks := []diffHunk{{
		OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1,
		Lines: []diffLine{
			{Op: '-', Text: "a"},
			{Op: '+', Text: "A"},
		},
	}}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(out, "\n") {
		t.Fatalf("should not add trailing newline: %q", out)
	}
}

func TestPatchFileViaRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Default(dir, nil, nil, nil, "", nil)
	diff := `@@ -2,1 +2,1 @@
-line2
+LINE2
`
	raw, _ := json.Marshal(map[string]string{"path": "f.txt", "diff": diff})
	out, err := r.Run(context.Background(), "patch_file", json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "patched f.txt") {
		t.Fatalf("got %q", out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "line1\nLINE2\nline3\n" {
		t.Fatalf("file: %q", string(b))
	}
}

func TestParseUnifiedDiff_EmptyContextLine(t *testing.T) {
	// A bare empty line inside a hunk should be treated as a context line (space-prefixed empty).
	// Models often emit bare empty lines instead of " " for empty file lines.
	diff := "@@ -1,3 +1,4 @@\n a\n\n c\n+d\n"
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	h := hunks[0]
	if h.OldCount != 3 || h.NewCount != 4 {
		t.Fatalf("counts: old=%d new=%d", h.OldCount, h.NewCount)
	}
	// Verify the empty line was parsed as a context line.
	if h.Lines[1].Op != ' ' || h.Lines[1].Text != "" {
		t.Fatalf("expected context empty line, got op=%c text=%q", h.Lines[1].Op, h.Lines[1].Text)
	}
}

func TestApplyHunks_EmptyContextLine(t *testing.T) {
	original := "a\n\nc\n"
	diff := "@@ -1,3 +1,4 @@\n a\n\n c\n+d\n"
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	out, err := applyHunks(original, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a\n\nc\nd\n" {
		t.Fatalf("got %q", out)
	}
}

func TestParseUnifiedDiff_EndToEnd(t *testing.T) {
	diff := `@@ -1,3 +1,3 @@
 a
-b
+b2
 c
`
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatal(err)
	}
	out, err := applyHunks("a\nb\nc\n", hunks)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a\nb2\nc\n" {
		t.Fatalf("got %q", out)
	}
}
