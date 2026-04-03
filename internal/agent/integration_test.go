//go:build integration

package agent_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"codient/internal/agent"
	"codient/internal/config"
	"codient/internal/lmstudio"
	"codient/internal/tools"
)

// End-to-end agent tests with a live OpenAI-compatible server.
//
// Run:
//
//	CODIENT_INTEGRATION=1 LMSTUDIO_MODEL=<id> go test -tags=integration ./internal/agent/...
//
// Tool-using tests use only the echo tool (no workspace). Model behavior may vary; failures
// often mean the model ignored tools or the prompt—adjust the system string for your model.

func TestIntegration_AgentDirectReply(t *testing.T) {
	if os.Getenv("CODIENT_INTEGRATION") != "1" {
		t.Skip("set CODIENT_INTEGRATION=1 to run live API tests")
	}
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	client := lmstudio.New(cfg)
	reg := tools.Default("", nil)
	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	reply, _, err := ar.Run(ctx, "You are a helpful assistant.", "Respond with exactly: AGENT_OK", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(reply)) < 4 {
		t.Fatalf("unexpectedly short reply: %q", reply)
	}
	upper := strings.ToUpper(reply)
	if !strings.Contains(upper, "AGENT") && !strings.Contains(upper, "OK") {
		t.Logf("model reply (may still be valid): %q", reply)
	}
	t.Logf("reply: %s", truncateRunes(reply, 500))
}

func TestIntegration_AgentUsesEchoTool(t *testing.T) {
	if os.Getenv("CODIENT_INTEGRATION") != "1" {
		t.Skip("set CODIENT_INTEGRATION=1 to run live API tests")
	}
	if os.Getenv("CODIENT_INTEGRATION_STRICT_TOOLS") != "1" {
		t.Skip("set CODIENT_INTEGRATION_STRICT_TOOLS=1 to enforce tool-calling (model-dependent)")
	}
	if testing.Short() {
		t.Skip("skipping live LLM call in -short mode")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	client := lmstudio.New(cfg)
	reg := tools.Default("", nil)
	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const mark = "CODIENT_TOOL_MARK_42"
	sys := "You have a function tool named echo. When the user asks you to echo a message, you MUST call echo with JSON {\"message\": <their exact string>} and nothing else until the tool returns."
	user := "Use the echo tool now with message exactly: " + mark
	reply, _, err := ar.Run(ctx, sys, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, mark) {
		t.Fatalf("expected final reply to contain tool output %q; got: %q", mark, reply)
	}
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
