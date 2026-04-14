package tools

import (
	"strings"
	"testing"
)

func TestRegisterWebSearch_NilOpts(t *testing.T) {
	r := NewRegistry()
	registerWebSearch(r, nil, nil)
	for _, n := range r.Names() {
		if n == "web_search" {
			t.Fatal("web_search should not be registered with nil opts")
		}
	}
}

func TestRegisterWebSearch_WithOpts(t *testing.T) {
	r := NewRegistry()
	registerWebSearch(r, &SearchOptions{}, nil)
	found := false
	for _, n := range r.Names() {
		if n == "web_search" {
			found = true
		}
	}
	if !found {
		t.Fatal("web_search should be registered with non-nil opts")
	}
}

func TestFormatSearchResults_Empty(t *testing.T) {
	out := formatSearchResults("test", nil)
	if !strings.Contains(out, "No results") {
		t.Errorf("expected no-results message, got: %s", out)
	}
}
