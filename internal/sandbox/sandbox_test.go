package sandbox

import (
	"testing"
)

func TestSelectRunner_Off(t *testing.T) {
	r := SelectRunner("off", SelectOptions{})
	if _, ok := r.(NoopRunner); !ok {
		t.Fatalf("want NoopRunner, got %T %s", r, r.Name())
	}
	if !r.Available() {
		t.Fatal("noop should be available")
	}
}

func TestSelectRunner_Auto(t *testing.T) {
	var warns []string
	r := SelectRunner("auto", SelectOptions{
		Warn: func(s string) { warns = append(warns, s) },
	})
	if r.Name() == "" {
		t.Fatal("expected a runner")
	}
	// Auto always returns an available runner (native, container, or noop).
	if !r.Available() {
		t.Fatal("auto should yield Available runner")
	}
}

func TestSelectRunner_Invalid(t *testing.T) {
	r := SelectRunner("bogus", SelectOptions{})
	if r.Available() {
		t.Fatal("invalid mode should not be available")
	}
	if _, ok := r.(errRunner); !ok {
		t.Fatalf("want errRunner, got %T", r)
	}
}

func TestModeIsValid(t *testing.T) {
	for _, s := range []string{"", "off", "native", "container", "auto", "none"} {
		if !ModeIsValid(s) {
			t.Fatalf("should be valid: %q", s)
		}
	}
	if ModeIsValid("bogus") {
		t.Fatal("bogus should be invalid")
	}
}
