package assistout

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
	"golang.org/x/term"
)

const welcomeTagline = "OpenAI-compatible coding agent"

func gradientText(s, fromHex, toHex string, bold bool) string {
	c1, err1 := colorful.Hex(fromHex)
	c2, err2 := colorful.Hex(toHex)
	if err1 != nil || err2 != nil {
		st := lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC"))
		if bold {
			st = st.Bold(true)
		}
		return st.Render(s)
	}
	rs := []rune(s)
	if len(rs) == 0 {
		return ""
	}
	denom := float64(len(rs) - 1)
	if denom < 1e-9 {
		denom = 1
	}
	var b strings.Builder
	for i, r := range rs {
		t := float64(i) / denom
		c := c1.BlendRgb(c2, t)
		hx := c.Hex()
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(hx))
		if bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(string(r)))
	}
	return b.String()
}

// gradientRasterLine applies a horizontal gradient to non-space runes (for block ASCII).
func gradientRasterLine(line, fromHex, toHex string, bold bool) string {
	c1, err1 := colorful.Hex(fromHex)
	c2, err2 := colorful.Hex(toHex)
	if err1 != nil || err2 != nil {
		return line
	}
	rs := []rune(line)
	if len(rs) == 0 {
		return ""
	}
	denom := float64(len(rs) - 1)
	if denom < 1e-9 {
		denom = 1
	}
	var b strings.Builder
	for i, r := range rs {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		t := float64(i) / denom
		c := c1.BlendRgb(c2, t)
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex()))
		if bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(string(r)))
	}
	return b.String()
}

// WelcomeParams configures the startup banner written to stderr.
type WelcomeParams struct {
	Plain     bool
	Quiet     bool
	Repl      bool
	Mode      string // build | ask | plan
	Workspace string
	Model     string
	// ResumeSummary is a one-line summary of the session being resumed (e.g. turns + last user message). Empty when not resuming.
	ResumeSummary string
}

// WriteWelcome prints a short colorful banner. Skipped when Quiet is true,
// except a single-line resume summary is still printed when ResumeSummary is set.
func WriteWelcome(w io.Writer, p WelcomeParams) {
	termWidth := stderrTerminalWidth()
	resumeSummary := formatResumeSummary(p.ResumeSummary, maxResumeSummaryWidth(termWidth))

	if p.Quiet {
		if resumeSummary != "" {
			fmt.Fprintf(w, "codient: resuming · %s\n", resumeSummary)
		}
		return
	}
	mode := strings.TrimSpace(p.Mode)
	if mode == "" {
		mode = "build"
	}
	run := "Run"
	if p.Repl {
		run = "Session"
	}
	ws := truncateWelcomePath(strings.TrimSpace(p.Workspace), 58)
	model := truncateWelcomePath(strings.TrimSpace(p.Model), 52)

	if p.Plain || !stderrInteractive() {
		for _, row := range codientBlockASCII {
			fmt.Fprintf(w, "  %s\n", row)
		}
		fmt.Fprintf(w, "  %s\n", welcomeTagline)
		fmt.Fprintf(w, "  %s · mode %s\n", run, mode)
		if ws != "" {
			fmt.Fprintf(w, "  %s\n", ws)
		}
		if model != "" {
			fmt.Fprintf(w, "  model %s\n", model)
		}
		if resumeSummary != "" {
			fmt.Fprintf(w, "  Resuming · %s\n", resumeSummary)
		}
		fmt.Fprintln(w)
		return
	}

	// Gemini-style cool → warm sweep; endpoints tuned per background.
	titleFrom, titleTo := "#0369A1", "#BE185D"
	ruleFrom, ruleTo := "#0284C7", "#C026D3"
	if lipgloss.HasDarkBackground() {
		titleFrom, titleTo = "#38BDF8", "#F472B6"
		ruleFrom, ruleTo = "#38BDF8", "#E879F9"
	}
	var blockLines []string
	for _, row := range codientBlockASCII {
		blockLines = append(blockLines, "  "+gradientRasterLine(row, titleFrom, titleTo, true))
	}
	block := strings.Join(blockLines, "\n")

	rule := gradientText(strings.Repeat("·", 38), ruleFrom, ruleTo, false)

	dim := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#525252", Dark: "#94A3B8"})

	modeHi := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#BE185D", Dark: "#E879F9"})

	line1 := dim.Render(run+" · mode ") + modeHi.Render(mode)
	boxLines := []string{"  " + line1}
	if ws != "" {
		boxLines = append(boxLines, "  "+dim.Render(ws))
	}
	if model != "" {
		boxLines = append(boxLines, "  "+dim.Render("model "+model))
	}
	boxInner := strings.Join(boxLines, "\n")

	header := strings.Join([]string{
		block,
		"  " + rule,
	}, "\n")

	boxed := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#C084FC"}).
		Padding(0, 1).
		Render(boxInner)

	if resumeSummary != "" {
		resumeLine := dim.Render("  Resuming · " + resumeSummary)
		fmt.Fprintf(w, "\n%s\n\n%s\n%s\n\n", header, boxed, resumeLine)
		return
	}
	fmt.Fprintf(w, "\n%s\n\n%s\n\n", header, boxed)
}

func stderrTerminalWidth() int {
	fd := int(os.Stderr.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil || w < 20 {
		return 0
	}
	return w
}

func maxResumeSummaryWidth(termWidth int) int {
	// Keep this comfortably below typical REPL widths to avoid wrapped box lines.
	max := 96
	if termWidth <= 0 {
		return max
	}
	// Reserve space for border/padding/left indent and "Resuming · " prefix.
	candidate := termWidth - 28
	if candidate < 40 {
		return 40
	}
	if candidate < max {
		return candidate
	}
	return max
}

func formatResumeSummary(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	return truncateWelcomeTail(s, max)
}

func truncateWelcomeTail(s string, max int) string {
	if s == "" || max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	rs := []rune(s)
	if max == 1 {
		return "…"
	}
	return strings.TrimSpace(string(rs[:max-1])) + "…"
}

func stderrInteractive() bool {
	st, err := os.Stderr.Stat()
	return err == nil && (st.Mode()&os.ModeCharDevice) != 0
}

func truncateWelcomePath(s string, max int) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	half := (max - 1) / 2
	if half < 1 {
		half = 1
	}
	return string(r[:half]) + "…" + string(r[len(r)-half:])
}
