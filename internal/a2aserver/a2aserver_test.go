package a2aserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/openai/openai-go/v3"

	"codient/internal/agent"
	"codient/internal/config"
	"codient/internal/prompt"
)

type mockLLM struct {
	model  string
	calls  int
	script []string
}

func (m *mockLLM) Model() string { return m.model }

func (m *mockLLM) ChatCompletion(_ context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
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

func textCompletion(content string) string {
	b, _ := json.Marshal(openai.ChatCompletion{
		ID:      "test",
		Object:  "chat.completion",
		Created: 1,
		Model:   "mock",
		Choices: []openai.ChatCompletionChoice{
			{
				Index:        0,
				FinishReason: "stop",
				Message:      openai.ChatCompletionMessage{Role: "assistant", Content: content},
			},
		},
	})
	return string(b)
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	ws := t.TempDir()
	return &config.Config{
		BaseURL:   "http://unused",
		APIKey:    "test-key",
		Model:     "mock",
		Workspace: ws,
	}
}

func startServer(t *testing.T, cfg *config.Config, llm *mockLLM) *httptest.Server {
	t.Helper()
	handler := New(Config{
		Cfg: cfg,
		LLMForMode: func(prompt.Mode) agent.ChatClient {
			return llm
		},
		Version: "test",
		Addr:    "127.0.0.1:0",
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func newClient(t *testing.T, serverURL string) *a2aclient.Client {
	t.Helper()
	ctx := context.Background()
	c, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
		a2a.NewAgentInterface(serverURL+"/a2a", a2a.TransportProtocolJSONRPC),
	})
	if err != nil {
		t.Fatalf("a2aclient.NewFromEndpoints: %v", err)
	}
	t.Cleanup(func() { c.Destroy() })
	return c
}

func TestAgentCard(t *testing.T) {
	cfg := testConfig(t)
	llm := &mockLLM{model: "mock", script: []string{textCompletion("hi")}}
	ts := startServer(t, cfg, llm)

	resp, err := http.Get(ts.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET agent card: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("agent card status: %d", resp.StatusCode)
	}

	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode agent card: %v", err)
	}
	if card.Name != "codient" {
		t.Errorf("card.Name = %q, want %q", card.Name, "codient")
	}
	if len(card.Skills) != 3 {
		t.Errorf("len(card.Skills) = %d, want 3", len(card.Skills))
	}
	if !card.Capabilities.Streaming {
		t.Error("card.Capabilities.Streaming should be true")
	}
}

func TestSendMessage(t *testing.T) {
	cfg := testConfig(t)
	llm := &mockLLM{model: "mock", script: []string{textCompletion("Hello from codient")}}
	ts := startServer(t, cfg, llm)
	client := newClient(t, ts.URL)

	ctx := context.Background()
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Write a hello world"))
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("result type = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("task state = %q, want %q", task.Status.State, a2a.TaskStateCompleted)
	}
	if len(task.Artifacts) == 0 {
		t.Fatal("expected at least one artifact")
	}

	foundText := false
	for _, art := range task.Artifacts {
		for _, p := range art.Parts {
			if txt, ok := p.Content.(a2a.Text); ok {
				if string(txt) == "Hello from codient" {
					foundText = true
				}
			}
		}
	}
	if !foundText {
		t.Error("artifact should contain the agent reply text")
	}
}

func TestSendMessage_EmptyPrompt(t *testing.T) {
	cfg := testConfig(t)
	llm := &mockLLM{model: "mock"}
	ts := startServer(t, cfg, llm)
	client := newClient(t, ts.URL)

	ctx := context.Background()
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("   "))
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("result type = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateFailed {
		t.Errorf("task state = %q, want %q", task.Status.State, a2a.TaskStateFailed)
	}
}

func TestSendMessage_AskMode(t *testing.T) {
	cfg := testConfig(t)
	llm := &mockLLM{model: "mock", script: []string{textCompletion("The function does X")}}
	ts := startServer(t, cfg, llm)
	client := newClient(t, ts.URL)

	ctx := context.Background()
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("What does foo() do?"))
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message:  msg,
		Metadata: map[string]any{"mode": "ask"},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("result type = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("task state = %q, want %q", task.Status.State, a2a.TaskStateCompleted)
	}
}

func TestSendStreamingMessage(t *testing.T) {
	cfg := testConfig(t)
	llm := &mockLLM{model: "mock", script: []string{textCompletion("streamed reply")}}
	ts := startServer(t, cfg, llm)
	client := newClient(t, ts.URL)

	ctx := context.Background()
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("do something"))

	var events []a2a.Event
	for ev, err := range client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: msg}) {
		if err != nil {
			t.Fatalf("stream event error: %v", err)
		}
		events = append(events, ev)
	}

	if len(events) < 3 {
		t.Fatalf("expected at least 3 events (submitted, working, artifact/completed), got %d", len(events))
	}

	// First event should be the submitted task.
	if _, ok := events[0].(*a2a.Task); !ok {
		t.Errorf("events[0] type = %T, want *a2a.Task (submitted)", events[0])
	}
}

func TestResolveMode(t *testing.T) {
	tests := []struct {
		meta map[string]any
		want prompt.Mode
	}{
		{nil, prompt.ModeBuild},
		{map[string]any{}, prompt.ModeBuild},
		{map[string]any{"mode": "ask"}, prompt.ModeAsk},
		{map[string]any{"mode": "plan"}, prompt.ModePlan},
		{map[string]any{"mode": "build"}, prompt.ModeBuild},
		{map[string]any{"mode": "invalid"}, prompt.ModeBuild},
		{map[string]any{"mode": 42}, prompt.ModeBuild},
	}
	for _, tt := range tests {
		got := resolveMode(tt.meta)
		if got != tt.want {
			t.Errorf("resolveMode(%v) = %q, want %q", tt.meta, got, tt.want)
		}
	}
}

func TestExtractText(t *testing.T) {
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello"), a2a.NewTextPart("world"))
	got := extractText(msg)
	if got != "hello\nworld" {
		t.Errorf("extractText = %q, want %q", got, "hello\nworld")
	}

	if extractText(nil) != "" {
		t.Error("extractText(nil) should return empty string")
	}
}
