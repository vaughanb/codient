package codientcli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIModel_OutputAppendsToViewport(t *testing.T) {
	ic := newInputCloser()
	m := newTUIModel(ic, "ask", true)

	// Simulate window size (makes model ready).
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(tuiModel)
	if !m.ready {
		t.Fatal("model should be ready after WindowSizeMsg")
	}

	// Send output.
	updated, _ = m.Update(tuiOutputMsg("hello world\n"))
	m = updated.(tuiModel)

	if !strings.Contains(m.content.String(), "hello world") {
		t.Fatalf("viewport content should contain output, got %q", m.content.String())
	}
}

func TestTUIModel_ModeChange(t *testing.T) {
	ic := newInputCloser()
	m := newTUIModel(ic, "ask", true)

	updated, _ := m.Update(tuiModeMsg("build"))
	m = updated.(tuiModel)

	if m.mode != "build" {
		t.Fatalf("mode should be build, got %q", m.mode)
	}
	if !strings.Contains(m.input.Prompt, "build") {
		t.Fatalf("prompt should contain build, got %q", m.input.Prompt)
	}
}

func TestTUIModel_WorkingStatus(t *testing.T) {
	ic := newInputCloser()
	m := newTUIModel(ic, "ask", true)

	// Make model ready so the spinner renders in the viewport.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(tuiModel)

	updated, cmd := m.Update(tuiWorkingMsg(true))
	m = updated.(tuiModel)
	if !m.working {
		t.Fatal("working should be true")
	}
	if cmd == nil {
		t.Fatal("should return a tick command when working starts")
	}
	if !strings.Contains(m.viewportContent(), "Agent is working") {
		t.Fatal("viewport content should contain spinner text while working")
	}

	updated, _ = m.Update(tuiWorkingMsg(false))
	m = updated.(tuiModel)
	if m.working {
		t.Fatal("working should be false")
	}
	if strings.Contains(m.viewportContent(), "Agent is working") {
		t.Fatal("viewport content should not contain spinner text when idle")
	}
}

func TestTUIModel_EnterSendsInput(t *testing.T) {
	ic := newInputCloser()
	m := newTUIModel(ic, "ask", true)

	// Type some text then press Enter.
	m.input.SetValue("test input")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)

	select {
	case got := <-ic.ch:
		if got != "test input" {
			t.Fatalf("got %q, want %q", got, "test input")
		}
	default:
		t.Fatal("expected input on channel")
	}

	if m.input.Value() != "" {
		t.Fatal("input should be cleared after Enter")
	}
}

func TestTUIModel_QuitMessage(t *testing.T) {
	ic := newInputCloser()
	m := newTUIModel(ic, "ask", true)

	updated, cmd := m.Update(tuiQuitMsg{exitCode: 0})
	m = updated.(tuiModel)
	if !m.quitting {
		t.Fatal("should be quitting")
	}
	if cmd == nil {
		t.Fatal("should return tea.Quit cmd")
	}
}

func TestTUIWriter_SendsOutput(t *testing.T) {
	// tuiWriter.Write should not panic with a nil prog (graceful no-op).
	w := &tuiWriter{}
	n, err := w.Write([]byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("got %d, want 4", n)
	}
}

func TestTUIModel_SpinnerTickAdvancesFrame(t *testing.T) {
	ic := newInputCloser()
	m := newTUIModel(ic, "ask", true)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(tuiModel)

	updated, _ = m.Update(tuiWorkingMsg(true))
	m = updated.(tuiModel)

	content0 := m.viewportContent()

	// Simulate a spinner tick.
	updated, cmd := m.Update(tuiSpinnerTickMsg{})
	m = updated.(tuiModel)

	content1 := m.viewportContent()
	if content0 == content1 {
		t.Fatal("spinner tick should change the viewport content")
	}
	if cmd == nil {
		t.Fatal("tick should schedule another tick while working")
	}

	// Tick while not working should be a no-op.
	updated, _ = m.Update(tuiWorkingMsg(false))
	m = updated.(tuiModel)
	_, cmd = m.Update(tuiSpinnerTickMsg{})
	if cmd != nil {
		t.Fatal("tick while not working should not schedule another tick")
	}
}

func TestSanitizePipeOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text unchanged",
			in:   "hello world\n",
			want: "hello world\n",
		},
		{
			name: "strips ESC[K",
			in:   "before\x1b[Kafter\n",
			want: "beforeafter\n",
		},
		{
			name: "strips ESC[0K",
			in:   "text\x1b[0Kmore\n",
			want: "textmore\n",
		},
		{
			name: "carriage return keeps last segment",
			in:   "old text\rnew text\n",
			want: "new text\n",
		},
		{
			name: "combined CR and ESC[K",
			in:   "\r\x1b[K⠋ spinner\r\x1b[K⠙ spinner\r\x1b[K\n",
			want: "\n",
		},
		{
			name: "CR before LF treated as overwrite",
			in:   "old\rnew\n",
			want: "new\n",
		},
		{
			name: "multiple lines with mixed sequences",
			in:   "clean\n\r\x1b[K⠋ working\nend\n",
			want: "clean\n⠋ working\nend\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizePipeOutput(tt.in)
			if got != tt.want {
				t.Errorf("sanitizePipeOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTUIModel_ViewContainsInput(t *testing.T) {
	ic := newInputCloser()
	m := newTUIModel(ic, "build", true)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(tuiModel)

	view := m.View()
	if !strings.Contains(view, "[build] > ") {
		t.Fatalf("view should contain prompt, got:\n%s", view)
	}
}
