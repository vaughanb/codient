package assistout

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// PlanAnswerPrefix returns text to print before stdin when the assistant is
// blocking on a clarifying answer ("Answer: ").
func PlanAnswerPrefix(plain bool) string {
	return planStyledLabel(plain, "Answer: ", lipgloss.AdaptiveColor{Light: "#0B6E99", Dark: "#7FD7FF"}, true)
}

// PlanFollowUpPrefix is used when the plan turn did not ask a blocking question
// (optional correction or exit).
func PlanFollowUpPrefix(plain bool) string {
	return planStyledLabel(plain, "Follow-up (or exit): ", lipgloss.AdaptiveColor{Light: "#555555", Dark: "#9CA3AF"}, false)
}

// PlanFirstMessagePrefix is used before the first stdin line when -repl had no -prompt seed.
func PlanFirstMessagePrefix(plain bool) string {
	return planStyledLabel(plain, "Message: ", lipgloss.AdaptiveColor{Light: "#444444", Dark: "#8B949E"}, false)
}

// PlanStdinPrompt picks the REPL line prefix from the last assistant reply.
func PlanStdinPrompt(plain bool, lastAssistantReply string) string {
	if lastAssistantReply == "" {
		return PlanFirstMessagePrefix(plain)
	}
	if ReplySignalsPlanWait(lastAssistantReply) {
		return PlanAnswerPrefix(plain)
	}
	return PlanFollowUpPrefix(plain)
}

func planStyledLabel(plain bool, label string, fg lipgloss.AdaptiveColor, bold bool) string {
	if plain {
		return label
	}
	st, err := os.Stderr.Stat()
	if err != nil || (st.Mode()&os.ModeCharDevice) == 0 {
		return label
	}
	s := lipgloss.NewStyle().Foreground(fg)
	if bold {
		s = s.Bold(true)
	}
	return s.Render(label)
}
