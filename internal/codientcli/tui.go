package codientcli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"codient/internal/assistout"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// inputCloser wraps a channel with sync.Once so it can be safely closed from
// multiple goroutines (Ctrl+C in the TUI model and the error/shutdown path in run.go).
type inputCloser struct {
	ch   chan string
	once sync.Once
}

func newInputCloser() *inputCloser {
	return &inputCloser{ch: make(chan string, 1)}
}

func (ic *inputCloser) Close() {
	ic.once.Do(func() { close(ic.ch) })
}

// TUI message types.
type (
	tuiOutputMsg     string // new text for the viewport
	tuiQuitMsg       struct{ exitCode int }
	tuiModeMsg       string // mode changed
	tuiWorkingMsg    bool   // true = agent working, false = idle
	tuiSpinnerTickMsg time.Time
)

var tuiSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// tuiModel is the Bubble Tea model for the interactive REPL.
// content is a pointer because strings.Builder must not be copied after first write,
// and Bubble Tea passes models by value through Update.
type tuiModel struct {
	viewport     viewport.Model
	input        textinput.Model
	inputCloser  *inputCloser
	content      *strings.Builder
	ready        bool
	quitting     bool
	mode         string
	plain        bool
	working      bool
	spinnerFrame int
	width        int
	height       int
}

const tuiFooterHeight = 3 // separator + input + safety margin

func newTUIModel(ic *inputCloser, mode string, plain bool) tuiModel {
	ti := textinput.New()
	ti.Prompt = modePrompt(mode, plain)
	ti.Focus()
	ti.CharLimit = 0 // unlimited

	return tuiModel{
		input:       ti,
		inputCloser: ic,
		content:     &strings.Builder{},
		mode:        mode,
		plain:       plain,
	}
}

func modePrompt(mode string, plain bool) string {
	return assistout.SessionPrompt(plain, mode)
}

func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

// tuiRecover logs a panic with its stack trace to a temp file and re-panics.
// This lets us capture the real cause since Bubble Tea's built-in recovery
// discards the stack.
func tuiRecover() {
	if r := recover(); r != nil {
		f, err := os.CreateTemp("", "codient-panic-*.txt")
		if err == nil {
			fmt.Fprintf(f, "panic: %v\n\n", r)
			// Capture stack by re-panicking inside a nested recover.
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, false)
			f.Write(buf[:n])
			f.Close()
		}
		panic(r) // re-panic so Bubble Tea sees it
	}
}

func (m tuiModel) Update(msg tea.Msg) (_ tea.Model, _ tea.Cmd) {
	defer tuiRecover()
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			text := m.input.Value()
			m.input.Reset()
			if m.inputCloser != nil {
				m.inputCloser.ch <- text
			}
			return m, nil
		case tea.KeyCtrlC:
			if m.inputCloser != nil {
				m.inputCloser.Close()
				m.inputCloser = nil
			}
			m.quitting = true
			return m, tea.Quit
		case tea.KeyPgUp:
			if m.ready {
				m.viewport.HalfViewUp()
			}
			return m, nil
		case tea.KeyPgDown:
			if m.ready {
				m.viewport.HalfViewDown()
			}
			return m, nil
		case tea.KeyUp:
			if m.ready {
				n := 3
				if !msg.Alt {
					n = 1
				}
				m.viewport.LineUp(n)
			}
			return m, nil
		case tea.KeyDown:
			if m.ready {
				n := 3
				if !msg.Alt {
					n = 1
				}
				m.viewport.LineDown(n)
			}
			return m, nil
		case tea.KeyHome:
			if m.ready {
				m.viewport.GotoTop()
			}
			return m, nil
		case tea.KeyEnd:
			if m.ready {
				m.viewport.GotoBottom()
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		vpHeight := max(1, msg.Height-tuiFooterHeight)
		if !m.ready {
			m.viewport = viewport.New(msg.Width, vpHeight)
			m.viewport.KeyMap = disabledViewportKeyMap()
			m.viewport.SetContent(m.content.String())
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpHeight
		}

	case tuiOutputMsg:
		m.content.WriteString(string(msg))
		if m.ready {
			m.syncViewport()
		}

	case tuiModeMsg:
		m.mode = string(msg)
		m.input.Prompt = modePrompt(m.mode, m.plain)

	case tuiWorkingMsg:
		m.working = bool(msg)
		if m.working {
			m.spinnerFrame = 0
			if m.ready {
				m.syncViewport()
			}
			cmds = append(cmds, m.spinnerTick())
		} else if m.ready {
			m.syncViewport()
		}

	case tuiSpinnerTickMsg:
		if m.working {
			m.spinnerFrame++
			if m.ready {
				m.syncViewport()
			}
			cmds = append(cmds, m.spinnerTick())
		}

	case tuiQuitMsg:
		m.quitting = true
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)

	// Only forward non-key messages to the viewport (window size, mouse scroll).
	// Key events go exclusively to the textinput to avoid the viewport's
	// default bindings (b=PageUp, f=PageDown, etc.) stealing typed characters.
	if m.ready {
		if _, isKey := msg.(tea.KeyMsg); !isKey {
			m.viewport, cmd = m.viewport.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

var statusBarStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#888888"})

// syncViewport updates the viewport content from the builder plus any spinner
// suffix, and auto-scrolls if the viewport was already at the bottom.
func (m *tuiModel) syncViewport() {
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.viewportContent())
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// viewportContent returns the accumulated output plus an animated spinner line
// when the agent is working.
func (m *tuiModel) viewportContent() string {
	s := m.content.String()
	if m.working {
		frame := tuiSpinnerFrames[m.spinnerFrame%len(tuiSpinnerFrames)]
		s += frame + " Agent is working…"
	}
	return s
}

func (m tuiModel) spinnerTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(t time.Time) tea.Msg {
		return tuiSpinnerTickMsg(t)
	})
}

func (m tuiModel) View() (_ string) {
	defer tuiRecover()
	if !m.ready {
		return "Initializing..."
	}
	sep := statusBarStyle.Render(strings.Repeat("─", m.width))
	return m.viewport.View() + "\n" + sep + "\n" + m.input.View()
}

// tuiWriter is an io.Writer that sends each Write to the Bubble Tea program
// as a tuiOutputMsg. It is safe for concurrent use.
type tuiWriter struct {
	prog *tea.Program
	mu   sync.Mutex
}

func (w *tuiWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.prog != nil {
		w.prog.Send(tuiOutputMsg(string(p)))
	}
	return len(p), nil
}

// tuiSetup holds all state needed to run the Bubble Tea TUI session.
type tuiSetup struct {
	prog    *tea.Program
	input   *inputCloser
	origOut *os.File
	origErr *os.File
	stdoutR *os.File
	stdoutW *os.File
	stderrR *os.File
	stderrW *os.File
	exitCode int
	done     chan struct{} // closed when the session goroutine exits
}

// initTUI creates the Bubble Tea program and redirects stdout/stderr into it.
// The caller must run the returned setup's start method in a goroutine to pump
// pipe output into the TUI, then call prog.Run() on the main goroutine.
func initTUI(mode string, plain bool) (*tuiSetup, error) {
	origOut := os.Stdout
	origErr := os.Stderr

	// Cache terminal state before redirecting file descriptors.
	stdoutTTY := isFileTTY(origOut)
	stderrTTY := isFileTTY(origErr)
	width := getTermWidth(origErr)
	darkBg := lipgloss.HasDarkBackground()

	// Create pipes to capture stdout/stderr.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	assistout.SetTUIOverride(assistout.NewTUIOverrideValues(stdoutTTY, stderrTTY, width, darkBg))
	tuiModeActive.Store(true)

	os.Stdout = stdoutW
	os.Stderr = stderrW

	ic := newInputCloser()
	model := newTUIModel(ic, mode, plain)
	prog := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithOutput(origErr),
	)

	ts := &tuiSetup{
		prog:    prog,
		input:   ic,
		origOut: origOut,
		origErr: origErr,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
		done:    make(chan struct{}),
	}
	return ts, nil
}

// reEraseLine matches ANSI "erase in line" sequences (ESC [ … K).
var reEraseLine = regexp.MustCompile(`\x1b\[\d*K`)

// sanitizePipeOutput strips terminal cursor-control sequences that would
// corrupt the viewport. Bare \r (carriage return) without a following \n
// means "overwrite current line"; we handle this by keeping only the text
// after the last \r on each visual line.
func sanitizePipeOutput(s string) string {
	s = reEraseLine.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// startPipeReaders launches goroutines that read from the captured stdout/stderr
// pipes and forward content to the TUI viewport. Call this before prog.Run().
func (ts *tuiSetup) startPipeReaders() {
	pump := func(r io.Reader) {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				clean := sanitizePipeOutput(string(buf[:n]))
				if clean != "" {
					ts.prog.Send(tuiOutputMsg(clean))
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pump(ts.stdoutR)
	go pump(ts.stderrR)
}

// cleanup restores original stdout/stderr and closes pipes.
// Safe to call multiple times.
func (ts *tuiSetup) cleanup() {
	os.Stdout = ts.origOut
	os.Stderr = ts.origErr
	// Close all pipe ends (Close on an already-closed *os.File is harmless).
	ts.stdoutW.Close()
	ts.stderrW.Close()
	ts.stdoutR.Close()
	ts.stderrR.Close()
	assistout.SetTUIOverride(nil)
	tuiModeActive.Store(false)
}

// disabledViewportKeyMap returns a KeyMap with all bindings disabled so the
// viewport never intercepts keystrokes meant for the text input.
func disabledViewportKeyMap() viewport.KeyMap {
	disabled := func() key.Binding { return key.NewBinding(key.WithDisabled()) }
	return viewport.KeyMap{
		PageDown:     disabled(),
		PageUp:       disabled(),
		HalfPageUp:   disabled(),
		HalfPageDown: disabled(),
		Down:         disabled(),
		Up:           disabled(),
		Left:         disabled(),
		Right:        disabled(),
	}
}

func isFileTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func getTermWidth(f *os.File) int {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return 80
	}
	w, _, err := term.GetSize(fd)
	if err != nil || w < 20 {
		return 80
	}
	return w
}
