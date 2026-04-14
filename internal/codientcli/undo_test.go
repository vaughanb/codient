package codientcli

import (
	"reflect"
	"sort"
	"testing"
)

func TestComputeUndoEntry_NewFiles(t *testing.T) {
	entry := computeUndoEntry(
		nil, nil,
		[]string{"a.go", "b.go"}, []string{"new.txt"},
		5,
	)
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	sort.Strings(entry.modifiedFiles)
	if !reflect.DeepEqual(entry.modifiedFiles, []string{"a.go", "b.go"}) {
		t.Fatalf("modifiedFiles = %v", entry.modifiedFiles)
	}
	if !reflect.DeepEqual(entry.createdFiles, []string{"new.txt"}) {
		t.Fatalf("createdFiles = %v", entry.createdFiles)
	}
	if entry.historyLen != 5 {
		t.Fatalf("historyLen = %d", entry.historyLen)
	}
}

func TestComputeUndoEntry_PreExistingUnchanged(t *testing.T) {
	entry := computeUndoEntry(
		[]string{"already.go"}, []string{"old.txt"},
		[]string{"already.go", "changed.go"}, []string{"old.txt", "brand_new.txt"},
		3,
	)
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if !reflect.DeepEqual(entry.modifiedFiles, []string{"changed.go"}) {
		t.Fatalf("modifiedFiles = %v", entry.modifiedFiles)
	}
	if !reflect.DeepEqual(entry.createdFiles, []string{"brand_new.txt"}) {
		t.Fatalf("createdFiles = %v", entry.createdFiles)
	}
}

func TestComputeUndoEntry_NoChanges(t *testing.T) {
	entry := computeUndoEntry(
		[]string{"a.go"}, []string{"b.txt"},
		[]string{"a.go"}, []string{"b.txt"},
		10,
	)
	if entry != nil {
		t.Fatalf("expected nil for no changes, got %+v", entry)
	}
}

func TestComputeUndoEntry_AllNil(t *testing.T) {
	entry := computeUndoEntry(nil, nil, nil, nil, 0)
	if entry != nil {
		t.Fatalf("expected nil, got %+v", entry)
	}
}

func TestComputeUndoEntry_OnlyModified(t *testing.T) {
	entry := computeUndoEntry(
		nil, nil,
		[]string{"x.go"}, nil,
		1,
	)
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if !reflect.DeepEqual(entry.modifiedFiles, []string{"x.go"}) {
		t.Fatalf("modifiedFiles = %v", entry.modifiedFiles)
	}
	if len(entry.createdFiles) != 0 {
		t.Fatalf("createdFiles = %v", entry.createdFiles)
	}
}

func TestComputeUndoEntry_OnlyCreated(t *testing.T) {
	entry := computeUndoEntry(
		nil, nil,
		nil, []string{"new.go"},
		2,
	)
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if len(entry.modifiedFiles) != 0 {
		t.Fatalf("modifiedFiles = %v", entry.modifiedFiles)
	}
	if !reflect.DeepEqual(entry.createdFiles, []string{"new.go"}) {
		t.Fatalf("createdFiles = %v", entry.createdFiles)
	}
}

func TestSetDiff(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want []string
	}{
		{"empty", nil, nil, nil},
		{"a only", []string{"x"}, nil, []string{"x"}},
		{"b only", nil, []string{"x"}, nil},
		{"overlap", []string{"a", "b", "c"}, []string{"b"}, []string{"a", "c"}},
		{"identical", []string{"a", "b"}, []string{"a", "b"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := setDiff(tt.a, tt.b)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("setDiff(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
