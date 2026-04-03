package assistout

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// WelcomeParams configures the startup banner written to stderr.
type WelcomeParams struct {
	Plain     bool
	Repl      bool
	Mode      string // agent | ask | plan
	Workspace string
	Model     string
}

// WriteWelcome prints a short colorful banner. Skipped when CODIENT_QUIET=1.
func WriteWelcome(w io.Writer, p WelcomeParams) {
	if strings.TrimSpace(os.Getenv("CODIENT_QUIET")) == "1" {
		return
	}
	mode := strings.TrimSpace(p.Mode)
	if mode == "" {
		mode = "agent"
	}
	run := "Run"
	if p.Repl {
		run = "REPL"
	}
	ws := truncateWelcomePath(strings.TrimSpace(p.Workspace), 58)
	model := truncateWelcomePath(strings.TrimSpace(p.Model), 52)

	if p.Plain || !stderrInteractive() {
		fmt.Fprintf(w, "codient — local LM coding agent\n")
		fmt.Fprintf(w, "  %s · mode %s\n", run, mode)
		if ws != "" {
			fmt.Fprintf(w, "  %s\n", ws)
		}
		if model != "" {
			fmt.Fprintf(w, "  model %s\n", model)
		}
		fmt.Fprintln(w)
		return
	}

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.AdaptiveColor{Light: "#0369A1", Dark: "#7DD3FC"}).
		Render("codient")

	mark := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"}).
		Render("◆")

	dim := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "#525252", Dark: "#94A3B8"})

	line1 := fmt.Sprintf("  %s  %s  %s", title, mark, dim.Render("local LM coding agent"))
	line2 := dim.Render(fmt.Sprintf("%s · mode %s", run, mode))
	lines := []string{line1, "  " + line2}
	if ws != "" {
		lines = append(lines, "  "+dim.Render(ws))
	}
	if model != "" {
		lines = append(lines, "  "+dim.Render("model "+model))
	}
	inner := strings.Join(lines, "\n")

	boxed := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "#0EA5E9", Dark: "#38BDF8"}).
		Padding(0, 1).
		Render(inner)

	fmt.Fprintf(w, "\n%s\n\n", boxed)
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
