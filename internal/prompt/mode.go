package prompt

import (
	"fmt"
	"os"
	"strings"
)

// Mode selects agent behavior (tools + system prompt). See -mode and CODIENT_MODE.
type Mode string

const (
	ModeAgent Mode = "agent"
	ModeAsk   Mode = "ask"
	ModePlan  Mode = "plan"
)

// ParseMode normalizes and validates a mode string. Empty string means agent.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "agent":
		return ModeAgent, nil
	case "ask":
		return ModeAsk, nil
	case "plan":
		return ModePlan, nil
	default:
		return "", fmt.Errorf("invalid mode %q (want agent, ask, or plan)", s)
	}
}

// ResolveMode uses flagValue when non-empty; otherwise CODIENT_MODE; default agent.
func ResolveMode(flagValue string) (Mode, error) {
	s := strings.TrimSpace(flagValue)
	if s == "" {
		s = strings.TrimSpace(os.Getenv("CODIENT_MODE"))
	}
	return ParseMode(s)
}
