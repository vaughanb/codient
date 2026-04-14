package sessionstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
)

func TestSaveAndLoad(t *testing.T) {
	tmp := t.TempDir()
	msgs := FromOpenAI([]openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("hello"),
		openai.AssistantMessage("hi there"),
	})
	state := &SessionState{
		ID:        "test_20260403_120000",
		Workspace: tmp,
		Mode:      "build",
		Model:     "test-model",
		CreatedAt: time.Now().UTC(),
		Messages:  msgs,
	}
	if err := Save(state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(tmp), state.ID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file not created: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != state.ID {
		t.Fatalf("ID mismatch: %q vs %q", loaded.ID, state.ID)
	}
	if loaded.Mode != "build" {
		t.Fatalf("mode: %q", loaded.Mode)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("messages: %d", len(loaded.Messages))
	}
}

func TestLoadLatest(t *testing.T) {
	tmp := t.TempDir()

	got, err := LoadLatest(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expected nil for empty dir")
	}

	s1 := &SessionState{ID: "s1", Workspace: tmp, Mode: "ask", CreatedAt: time.Now().UTC()}
	if err := Save(s1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	s2 := &SessionState{ID: "s2", Workspace: tmp, Mode: "build", CreatedAt: time.Now().UTC()}
	if err := Save(s2); err != nil {
		t.Fatal(err)
	}

	latest, err := LoadLatest(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != "s2" {
		t.Fatalf("expected s2, got %v", latest)
	}
}

func TestFromOpenAI_ToOpenAI_Roundtrip(t *testing.T) {
	original := []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("question"),
		openai.AssistantMessage("answer"),
		openai.ToolMessage("result", "call-1"),
	}
	stored := FromOpenAI(original)
	if len(stored) != 3 {
		t.Fatalf("stored len: %d", len(stored))
	}

	back, err := ToOpenAI(stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 3 {
		t.Fatalf("roundtrip len: %d", len(back))
	}
	if back[0].OfUser == nil {
		t.Fatal("expected user message")
	}
	if back[1].OfAssistant == nil {
		t.Fatal("expected assistant message")
	}
	if back[2].OfTool == nil {
		t.Fatal("expected tool message")
	}
}

func TestMessageRole(t *testing.T) {
	msgs := FromOpenAI([]openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("test"),
		openai.AssistantMessage("reply"),
	})
	if r := MessageRole(msgs[0]); r != "user" {
		t.Fatalf("expected user, got %q", r)
	}
	if r := MessageRole(msgs[1]); r != "assistant" {
		t.Fatalf("expected assistant, got %q", r)
	}
}

func TestMessageContent(t *testing.T) {
	msgs := FromOpenAI([]openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("hello world"),
	})
	if c := MessageContent(msgs[0]); c != "hello world" {
		t.Fatalf("expected 'hello world', got %q", c)
	}
}

func TestList(t *testing.T) {
	tmp := t.TempDir()
	s1 := &SessionState{ID: "older", Workspace: tmp, Mode: "ask",
		Messages: FromOpenAI([]openai.ChatCompletionMessageParamUnion{openai.UserMessage("a")})}
	if err := Save(s1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	s2 := &SessionState{ID: "newer", Workspace: tmp, Mode: "build",
		Messages: FromOpenAI([]openai.ChatCompletionMessageParamUnion{openai.UserMessage("b"), openai.UserMessage("c")})}
	if err := Save(s2); err != nil {
		t.Fatal(err)
	}

	summaries, err := List(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
	if summaries[0].ID != "newer" {
		t.Fatalf("expected newest first, got %q", summaries[0].ID)
	}
	if summaries[0].Turns != 2 {
		t.Fatalf("turns: %d", summaries[0].Turns)
	}
}

func TestNewID(t *testing.T) {
	id := NewID("/home/user/myproject")
	if !strings.HasPrefix(id, "myproject_") {
		t.Fatalf("unexpected id: %q", id)
	}
}

func TestDir(t *testing.T) {
	d := Dir("/tmp/ws")
	want := filepath.Join("/tmp/ws", ".codient", "sessions")
	if d != want {
		t.Fatalf("got %q want %q", d, want)
	}
}

func TestResumeSummaryLine(t *testing.T) {
	if g := ResumeSummaryLine("sess_1", nil); g != "" {
		t.Fatalf("empty msgs: got %q", g)
	}
	msgs := FromOpenAI([]openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("hello world"),
		openai.AssistantMessage("hi"),
	})
	s := ResumeSummaryLine("myproj_20260101_120000", msgs)
	if !strings.Contains(s, "session myproj_20260101_120000") || !strings.Contains(s, "1 turn") || !strings.Contains(s, "last: hello world") {
		t.Fatalf("got %q", s)
	}

	msgs2 := FromOpenAI([]openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("line one\nline two"),
	})
	s2 := ResumeSummaryLine("", msgs2)
	if !strings.Contains(s2, "line one") || strings.Contains(s2, "line two") {
		t.Fatalf("want first line only: %q", s2)
	}

	msgs3 := FromOpenAI([]openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("first"),
		openai.AssistantMessage("ok"),
		openai.UserMessage("second ask"),
	})
	s3 := ResumeSummaryLine("id", msgs3)
	if !strings.Contains(s3, "2 turns") || !strings.Contains(s3, "second ask") || strings.Contains(s3, "first") {
		t.Fatalf("want last user: %q", s3)
	}

	msgs4 := FromOpenAI([]openai.ChatCompletionMessageParamUnion{
		openai.UserMessage("normal user request"),
		openai.AssistantMessage("ok"),
		openai.UserMessage("You just provided suggestions. Before I accept them, try to DISPROVE each one using tool calls:"),
	})
	s4 := ResumeSummaryLine("id", msgs4)
	if !strings.Contains(s4, "normal user request") || strings.Contains(s4, "You just provided suggestions") {
		t.Fatalf("should skip internal verification prompt in preview: %q", s4)
	}
}
