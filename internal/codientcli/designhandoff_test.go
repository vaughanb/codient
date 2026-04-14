package codientcli

import (
	"strings"
	"testing"
)

var testTools = []string{"read_file", "write_file", "str_replace", "list_dir"}

func TestDesignHandoffUserMessage_IncludesDesignBody(t *testing.T) {
	const body = "UNIQUE_DESIGN_MARKER_9a2f"
	out := designHandoffUserMessage("## Goal\n"+body, testTools)
	if !strings.Contains(out, body) {
		t.Fatalf("expected design body preserved; got:\n%s", out)
	}
}

func TestDesignHandoffUserMessage_Directives(t *testing.T) {
	out := designHandoffUserMessage("# Ready to implement\n\n- [ ] step", testTools)
	lower := strings.ToLower(out)
	for _, phrase := range []string{
		"build mode",
		"do not ask",
		"confirmed",
		"start implementing",
		"verify",
		"ignore any line",
		"run codient",
	} {
		if !strings.Contains(lower, strings.ToLower(phrase)) {
			t.Errorf("expected message to contain %q\n---\n%s", phrase, out)
		}
	}
}

func TestDesignHandoffUserMessage_ListsTools(t *testing.T) {
	out := designHandoffUserMessage("design", testTools)
	for _, tool := range testTools {
		if !strings.Contains(out, tool) {
			t.Errorf("expected tool %q in message", tool)
		}
	}
	if strings.Contains(out, "run_command") {
		t.Error("should not mention run_command when not in tool list")
	}
}

func TestDesignHandoffUserMessage_TrimsDesignWhitespace(t *testing.T) {
	out := designHandoffUserMessage("  \n  hello  \n  ", testTools)
	if !strings.Contains(out, "hello") {
		t.Fatal(out)
	}
}
