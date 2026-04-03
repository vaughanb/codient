package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/config"
	"codient/internal/tools"
)

type mockLLM struct {
	model string
	calls int
	// script returns JSON completions in order (tool round then final text, etc.)
	script []string
}

func (m *mockLLM) Model() string { return m.model }

func (m *mockLLM) ChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	if m.calls >= len(m.script) {
		return nil, context.Canceled
	}
	raw := m.script[m.calls]
	m.calls++
	var out openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func TestRunner_DirectReply(t *testing.T) {
	js := `{
  "id": "x",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "hello user"
    },
    "finish_reason": "stop"
  }]
}`
	llm := &mockLLM{model: "m", script: []string{js}}
	reg := tools.NewRegistry()
	cfg := &config.Config{MaxToolSteps: 5}
	r := &Runner{LLM: llm, Cfg: cfg, Tools: reg}
	out, _, err := r.Run(context.Background(), "", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello user" {
		t.Fatalf("got %q", out)
	}
}

func TestRunner_ToolThenReply(t *testing.T) {
	toolRound := `{
  "id": "x",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "",
      "tool_calls": [{
        "id": "call_1",
        "type": "function",
        "function": {
          "name": "echo",
          "arguments": "{\"message\":\"tool-out\"}"
        }
      }]
    },
    "finish_reason": "tool_calls"
  }]
}`
	final := `{
  "id": "y",
  "object": "chat.completion",
  "created": 2,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "done"
    },
    "finish_reason": "stop"
  }]
}`
	llm := &mockLLM{model: "m", script: []string{toolRound, final}}
	reg := tools.NewRegistry()
	reg.Register(mustEchoTool(t))
	cfg := &config.Config{MaxToolSteps: 5}
	r := &Runner{LLM: llm, Cfg: cfg, Tools: reg}
	out, _, err := r.Run(context.Background(), "", "call echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("got %q", out)
	}
	if llm.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", llm.calls)
	}
}

func TestRunner_ToolErrorSurfaced(t *testing.T) {
	toolRound := `{
  "id": "x",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "",
      "tool_calls": [{
        "id": "call_1",
        "type": "function",
        "function": {
          "name": "missing_tool",
          "arguments": "{}"
        }
      }]
    },
    "finish_reason": "tool_calls"
  }]
}`
	final := `{
  "id": "y",
  "object": "chat.completion",
  "created": 2,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "handled"
    },
    "finish_reason": "stop"
  }]
}`
	llm := &mockLLM{model: "m", script: []string{toolRound, final}}
	reg := tools.NewRegistry()
	reg.Register(mustEchoTool(t))
	cfg := &config.Config{MaxToolSteps: 5}
	r := &Runner{LLM: llm, Cfg: cfg, Tools: reg}
	out, _, err := r.Run(context.Background(), "", "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "handled" {
		t.Fatalf("got %q", out)
	}
}

func TestRunner_EmptyChoices(t *testing.T) {
	js := `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[]}`
	llm := &mockLLM{model: "m", script: []string{js}}
	r := &Runner{LLM: llm, Cfg: &config.Config{MaxToolSteps: 3}, Tools: tools.NewRegistry()}
	_, _, err := r.Run(context.Background(), "", "hi", nil)
	if err == nil || !strings.Contains(err.Error(), "empty choices") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunner_MaxToolSteps(t *testing.T) {
	toolRound := `{
  "id": "x",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "",
      "tool_calls": [{
        "id": "call_1",
        "type": "function",
        "function": {"name": "echo", "arguments": "{\"message\":\"x\"}"}
      }]
    },
    "finish_reason": "tool_calls"
  }]
}`
	llm := &mockLLM{model: "m", script: []string{toolRound, toolRound, toolRound}}
	reg := tools.NewRegistry()
	reg.Register(mustEchoTool(t))
	cfg := &config.Config{MaxToolSteps: 2}
	r := &Runner{LLM: llm, Cfg: cfg, Tools: reg}
	_, _, err := r.Run(context.Background(), "", "loop", nil)
	if err == nil || !strings.Contains(err.Error(), "AGENT_MAX_TOOL_STEPS") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunner_SystemPrompt(t *testing.T) {
	js := `{
  "id": "x",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "ok"},
    "finish_reason": "stop"
  }]
}`
	llm := &mockLLM{model: "m", script: []string{js}}
	r := &Runner{LLM: llm, Cfg: &config.Config{MaxToolSteps: 3}, Tools: tools.NewRegistry()}
	_, _, err := r.Run(context.Background(), "sys", "user", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func mustEchoTool(t *testing.T) tools.Tool {
	t.Helper()
	return tools.Tool{
		Name:        "echo",
		Description: "echo",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
			},
			"required":             []string{"message"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			return p.Message, nil
		},
	}
}
