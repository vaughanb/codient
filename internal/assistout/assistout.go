// Package assistout formats assistant messages for the terminal (markdown + ANSI via glamour).
package assistout

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// tuiOverride, when non-nil, replaces live os.Stdout/os.Stderr TTY checks
// with cached values so that the Bubble Tea TUI (which redirects the file
// descriptors to pipes) still gets correct styling and word-wrap behaviour.
var tuiOverride atomic.Pointer[tuiOverrideValues]

type tuiOverrideValues struct {
	StdoutInteractive bool
	StderrInteractive bool
	TermWidth         int
	DarkBackground    bool
}

// SetTUIOverride installs cached TTY values for the lifetime of the TUI.
// Pass nil to clear the override (e.g. on TUI shutdown).
func SetTUIOverride(v *tuiOverrideValues) { tuiOverride.Store(v) }

// NewTUIOverrideValues constructs a tuiOverrideValues (exported for codientcli).
func NewTUIOverrideValues(stdoutTTY, stderrTTY bool, width int, darkBg bool) *tuiOverrideValues {
	return &tuiOverrideValues{
		StdoutInteractive: stdoutTTY,
		StderrInteractive: stderrTTY,
		TermWidth:         width,
		DarkBackground:    darkBg,
	}
}

// IsDarkBackground reports whether the terminal has a dark background.
// Uses the cached value in TUI mode (since the real fds are pipes).
func IsDarkBackground() bool {
	if o := tuiOverride.Load(); o != nil {
		return o.DarkBackground
	}
	return lipgloss.HasDarkBackground()
}

// StdoutIsInteractive reports whether os.Stdout is a character device (TTY).
func StdoutIsInteractive() bool {
	if o := tuiOverride.Load(); o != nil {
		return o.StdoutInteractive
	}
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (st.Mode() & os.ModeCharDevice) != 0
}

// terminalWordWrap returns a reasonable wrap width for glamour (defaults 80).
func terminalWordWrap() int {
	const defaultW = 80
	if o := tuiOverride.Load(); o != nil {
		if o.TermWidth >= 20 {
			return o.TermWidth
		}
		return defaultW
	}
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return defaultW
	}
	w, _, err := term.GetSize(fd)
	if err != nil || w < 20 {
		return defaultW
	}
	return w
}

// PrepareAssistantText applies plan-mode normalization (Question heading, option layout)
// without rendering. Use for REPL state (e.g. detecting blocking questions) when the
// reply was already printed via streaming.
func PrepareAssistantText(text string, planMode bool) string {
	text = strings.TrimRight(text, "\n")
	if planMode {
		text = InsertPlanQuestionHeading(text)
		text = NormalizePlanQuestionOptionLines(text)
	}
	return text
}

// WriteAssistant writes the assistant reply. When useMarkdown is true, renders
// GitHub-flavored markdown with colors and syntax-highlighted code blocks; otherwise
// prints plain text (for pipes, logs, or when -plain / config plain is set).
// When planMode is true, ensures a "## Question" heading before the wait line when missing
// so terminals show a styled Question section (see InsertPlanQuestionHeading).
func WriteAssistant(w io.Writer, text string, useMarkdown, planMode bool) error {
	text = PrepareAssistantText(text, planMode)
	if !useMarkdown {
		_, err := fmt.Fprintln(w, text)
		return err
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(terminalWordWrap()),
		glamour.WithEmoji(),
	)
	if err != nil {
		_, err2 := fmt.Fprintln(w, text)
		return err2
	}
	out, err := r.Render(text)
	if err != nil {
		_, err2 := fmt.Fprintln(w, text)
		return err2
	}
	_, err = fmt.Fprint(w, out)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(out, "\n") {
		_, err = fmt.Fprint(w, "\n")
	}
	return err
}
