package agent

import (
	"context"
	"encoding/json"
	"io"
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

// assertStreamUnusedLLM records whether ChatCompletionStream was invoked. The agent must use
// non-streaming ChatCompletion when the request includes tools and StreamWithTools is false
// (local servers often drop tool_calls over SSE).
type assertStreamUnusedLLM struct {
	t           *testing.T
	model       string
	script      []string
	calls       int
	streamCalls int
}

func (m *assertStreamUnusedLLM) Model() string { return m.model }

func (m *assertStreamUnusedLLM) ChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
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

func (m *assertStreamUnusedLLM) ChatCompletionStream(ctx context.Context, params openai.ChatCompletionNewParams, w io.Writer) (*openai.ChatCompletion, error) {
	m.streamCalls++
	m.t.Fatalf("ChatCompletionStream should not run for tool requests when StreamWithTools is false (would drop tool_calls on many local servers)")
	return nil, context.Canceled
}

func TestRunner_WithStreamWriterUsesChatCompletionWhenToolsPresent(t *testing.T) {
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
	llm := &assertStreamUnusedLLM{t: t, model: "m", script: []string{toolRound, final}}
	reg := tools.NewRegistry()
	reg.Register(mustEchoTool(t))
	cfg := &config.Config{StreamWithTools: false}
	r := &Runner{LLM: llm, Cfg: cfg, Tools: reg}
	out, _, err := r.Run(context.Background(), "", "call echo", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("got %q", out)
	}
	if llm.streamCalls != 0 {
		t.Fatalf("expected no streaming calls, got %d", llm.streamCalls)
	}
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
	cfg := &config.Config{}
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
	cfg := &config.Config{}
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
	cfg := &config.Config{}
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
	r := &Runner{LLM: llm, Cfg: &config.Config{}, Tools: tools.NewRegistry()}
	_, _, err := r.Run(context.Background(), "", "hi", nil)
	if err == nil || !strings.Contains(err.Error(), "empty choices") {
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
	r := &Runner{LLM: llm, Cfg: &config.Config{}, Tools: tools.NewRegistry()}
	_, _, err := r.Run(context.Background(), "sys", "user", nil)
	if err != nil {
		t.Fatal(err)
	}
}

type captureLLM struct {
	model  string
	script []string
	calls  int
	// MsgJSON is the JSON encoding of params.Messages for each ChatCompletion call.
	MsgJSON []json.RawMessage
}

func (c *captureLLM) Model() string { return c.model }

func (c *captureLLM) ChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	if c.calls >= len(c.script) {
		return nil, context.Canceled
	}
	rawMsgs, _ := json.Marshal(params.Messages)
	c.MsgJSON = append(c.MsgJSON, rawMsgs)
	raw := c.script[c.calls]
	c.calls++
	var out openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func mustWriteFileTool(t *testing.T) tools.Tool {
	t.Helper()
	return tools.Tool{
		Name:        "write_file",
		Description: "write",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			return "wrote f (overwrite)", nil
		},
	}
}

func mustReadFileTool(t *testing.T) tools.Tool {
	t.Helper()
	return tools.Tool{
		Name:        "read_file",
		Description: "read",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			return "ok", nil
		},
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

func TestRunner_AutoCheckInjectsOnFailure(t *testing.T) {
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
          "name": "write_file",
          "arguments": "{\"path\":\"f.txt\",\"content\":\"x\"}"
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
      "content": "fixed"
    },
    "finish_reason": "stop"
  }]
}`
	llm := &captureLLM{model: "m", script: []string{toolRound, final}}
	reg := tools.NewRegistry()
	reg.Register(mustWriteFileTool(t))
	cfg := &config.Config{}
	r := &Runner{
		LLM: llm, Cfg: cfg, Tools: reg,
		AutoCheck: func(context.Context) AutoCheckOutcome {
			return AutoCheckOutcome{Inject: "[auto-check] BUILD FAIL", Progress: "auto-check: test · exit=1"}
		},
	}
	_, _, err := r.Run(context.Background(), "", "edit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.MsgJSON) < 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llm.MsgJSON))
	}
	if !strings.Contains(string(llm.MsgJSON[1]), "[auto-check] BUILD FAIL") {
		t.Fatalf("second request should include auto-check inject: %s", string(llm.MsgJSON[1]))
	}
}

func TestRunner_AutoCheckSilentOnSuccess(t *testing.T) {
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
          "name": "write_file",
          "arguments": "{\"path\":\"f.txt\",\"content\":\"x\"}"
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
	llm := &captureLLM{model: "m", script: []string{toolRound, final}}
	reg := tools.NewRegistry()
	reg.Register(mustWriteFileTool(t))
	cfg := &config.Config{}
	r := &Runner{
		LLM: llm, Cfg: cfg, Tools: reg,
		AutoCheck: func(context.Context) AutoCheckOutcome {
			return AutoCheckOutcome{Progress: "auto-check: ok"}
		},
	}
	_, _, err := r.Run(context.Background(), "", "edit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.MsgJSON) < 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llm.MsgJSON))
	}
	if strings.Contains(string(llm.MsgJSON[1]), "[auto-check]") {
		t.Fatalf("should not inject on success: %s", string(llm.MsgJSON[1]))
	}
}

func TestRunner_AutoCheckSkipsReadOnly(t *testing.T) {
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
          "name": "read_file",
          "arguments": "{\"path\":\"f.txt\"}"
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
	llm := &captureLLM{model: "m", script: []string{toolRound, final}}
	reg := tools.NewRegistry()
	reg.Register(mustReadFileTool(t))
	cfg := &config.Config{}
	var runs int
	r := &Runner{
		LLM: llm, Cfg: cfg, Tools: reg,
		AutoCheck: func(context.Context) AutoCheckOutcome {
			runs++
			return AutoCheckOutcome{Inject: "should not run"}
		},
	}
	out, _, err := r.Run(context.Background(), "", "read", nil)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("auto-check should not run for read-only tools, runs=%d", runs)
	}
	if out != "done" {
		t.Fatalf("got %q", out)
	}
}

func TestRunner_PostReplyCheckInjects(t *testing.T) {
	first := `{
  "id": "a",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "initial suggestions"
    },
    "finish_reason": "stop"
  }]
}`
	second := `{
  "id": "b",
  "object": "chat.completion",
  "created": 2,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "verified summary"
    },
    "finish_reason": "stop"
  }]
}`
	llm := &captureLLM{model: "m", script: []string{first, second}}
	reg := tools.NewRegistry()
	cfg := &config.Config{}
	r := &Runner{
		LLM: llm, Cfg: cfg, Tools: reg,
		PostReplyCheck: func(_ context.Context, info PostReplyCheckInfo) string {
			if strings.Contains(info.Reply, "initial") {
				return "[verify] check your suggestions"
			}
			return ""
		},
	}
	out, _, err := r.Run(context.Background(), "", "suggest improvements", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "verified summary" {
		t.Fatalf("expected final reply from second call, got %q", out)
	}
	if llm.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", llm.calls)
	}
	if !strings.Contains(string(llm.MsgJSON[1]), "[verify] check your suggestions") {
		t.Fatalf("second request should contain injected verification message: %s", string(llm.MsgJSON[1]))
	}
}

func TestRunner_PostReplyCheckFiresOnce(t *testing.T) {
	first := `{
  "id": "a",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "suggestions"
    },
    "finish_reason": "stop"
  }]
}`
	second := `{
  "id": "b",
  "object": "chat.completion",
  "created": 2,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "final answer"
    },
    "finish_reason": "stop"
  }]
}`
	llm := &captureLLM{model: "m", script: []string{first, second}}
	reg := tools.NewRegistry()
	cfg := &config.Config{}
	var calls int
	r := &Runner{
		LLM: llm, Cfg: cfg, Tools: reg,
		PostReplyCheck: func(_ context.Context, _ PostReplyCheckInfo) string {
			calls++
			return "verify"
		},
	}
	out, _, err := r.Run(context.Background(), "", "suggest", nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("PostReplyCheck should fire exactly once, got %d", calls)
	}
	if out != "final answer" {
		t.Fatalf("expected second reply, got %q", out)
	}
}

func TestRunner_PostReplyCheckNilNoop(t *testing.T) {
	js := `{
  "id": "x",
  "object": "chat.completion",
  "created": 1,
  "model": "m",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "direct answer"
    },
    "finish_reason": "stop"
  }]
}`
	llm := &mockLLM{model: "m", script: []string{js}}
	reg := tools.NewRegistry()
	cfg := &config.Config{}
	r := &Runner{LLM: llm, Cfg: cfg, Tools: reg}
	out, _, err := r.Run(context.Background(), "", "question", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "direct answer" {
		t.Fatalf("got %q", out)
	}
	if llm.calls != 1 {
		t.Fatalf("expected 1 LLM call, got %d", llm.calls)
	}
}
