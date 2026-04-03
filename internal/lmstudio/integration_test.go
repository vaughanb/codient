//go:build integration

package lmstudio_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/config"
	"codient/internal/lmstudio"
)

// Live tests against a running OpenAI-compatible server (e.g. LM Studio).
//
// Run:
//
//	CODIENT_INTEGRATION=1 LMSTUDIO_MODEL=<id> go test -tags=integration ./internal/lmstudio/...
//
// Optional: LMSTUDIO_BASE_URL (see config.Load).

func TestIntegration_PingModels(t *testing.T) {
	if os.Getenv("CODIENT_INTEGRATION") != "1" {
		t.Skip("set CODIENT_INTEGRATION=1 to run live API tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	c := lmstudio.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.PingModels(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_ListModelsIncludesConfiguredModel(t *testing.T) {
	if os.Getenv("CODIENT_INTEGRATION") != "1" {
		t.Skip("set CODIENT_INTEGRATION=1 to run live API tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireModel(); err != nil {
		t.Fatal(err)
	}
	c := lmstudio.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ids, err := c.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := cfg.Model
	for _, id := range ids {
		if id == want {
			return
		}
	}
	t.Fatalf("model %q not found in /v1/models; got %v", want, ids)
}

func TestIntegration_ChatCompletionNonEmpty(t *testing.T) {
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
	c := lmstudio.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := c.ChatCompletion(ctx, openai.ChatCompletionNewParams{
		Model: shared.ChatModel(cfg.Model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Say the single word OK and nothing else."),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Choices) == 0 {
		t.Fatal("no choices")
	}
	content := res.Choices[0].Message.Content
	if len(strings.TrimSpace(content)) < 2 {
		t.Fatalf("unexpectedly short model reply: %q", content)
	}
	t.Logf("model reply (%d runes): %s", len([]rune(content)), truncateRunes(content, 200))
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
