// Package tokentracker accumulates API-reported token usage for a REPL session.
package tokentracker

import (
	"fmt"
	"strings"
	"sync"
)

// Usage holds token counts from a single completion or cumulative session totals.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// Tracker is a thread-safe accumulator for session token usage.
type Tracker struct {
	mu       sync.Mutex
	session  Usage
	lastCall Usage
	turnMark Usage
}

// Add merges usage from one LLM completion into the session total.
func (t *Tracker) Add(u Usage) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastCall = u
	t.session.PromptTokens += u.PromptTokens
	t.session.CompletionTokens += u.CompletionTokens
	t.session.TotalTokens += u.TotalTokens
}

// Last returns the most recent completion's usage (after the last Add).
func (t *Tracker) Last() Usage {
	if t == nil {
		return Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastCall
}

// Session returns cumulative usage for the current session.
func (t *Tracker) Session() Usage {
	if t == nil {
		return Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.session
}

// Reset clears session and turn markers (e.g. /clear, /new).
func (t *Tracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.session = Usage{}
	t.lastCall = Usage{}
	t.turnMark = Usage{}
}

// MarkTurnStart snapshots session totals at the start of a user turn for TurnSinceMark.
func (t *Tracker) MarkTurnStart() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turnMark = t.session
}

// TurnSinceMark returns token usage accumulated since the last MarkTurnStart.
func (t *Tracker) TurnSinceMark() Usage {
	if t == nil {
		return Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return subUsage(t.session, t.turnMark)
}

func subUsage(a, b Usage) Usage {
	return Usage{
		PromptTokens:     a.PromptTokens - b.PromptTokens,
		CompletionTokens: a.CompletionTokens - b.CompletionTokens,
		TotalTokens:      a.TotalTokens - b.TotalTokens,
	}
}

// HasAny returns true if any token count is non-zero.
func (u Usage) HasAny() bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// FormatLine formats a single-line token summary for progress output.
func FormatLine(u Usage) string {
	return FormatLineCtx(u, 0)
}

// FormatLineCtx formats a single-line token summary including context usage
// percentage when contextWindow > 0.
func FormatLineCtx(u Usage, contextWindow int) string {
	if !u.HasAny() {
		return ""
	}
	s := fmt.Sprintf("tokens: %s in / %s out",
		formatTokenCount(u.PromptTokens),
		formatTokenCount(u.CompletionTokens))
	if contextWindow > 0 && u.PromptTokens > 0 {
		pct := float64(u.PromptTokens) / float64(contextWindow) * 100
		s += fmt.Sprintf(" (%d%% ctx)", int(pct))
	}
	return s
}

// FormatTurnSummary formats a turn footer line including optional estimated cost.
// When turn is 0, omits the turn label (non-REPL single-shot mode).
func FormatTurnSummary(turn int, u Usage, estCost float64, hasPrice bool) string {
	if !u.HasAny() {
		return ""
	}
	in := formatTokenCount(u.PromptTokens)
	out := formatTokenCount(u.CompletionTokens)
	var line string
	if turn > 0 {
		line = fmt.Sprintf("  turn %d: %s in / %s out", turn, in, out)
	} else {
		line = fmt.Sprintf("  tokens: %s in / %s out", in, out)
	}
	if hasPrice {
		line += fmt.Sprintf(" · est. %s", FormatUSD(estCost))
	}
	return line
}

// formatTokenCount renders integers with k/M suffixes for readability.
func formatTokenCount(n int64) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n >= 1000:
		return fmt.Sprintf("%.2fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// FormatUSD prints a dollar amount with cents when small.
func FormatUSD(x float64) string {
	if x < 0 {
		x = -x
	}
	if x >= 0.01 || x == 0 {
		return fmt.Sprintf("$%.4f", x)
	}
	return fmt.Sprintf("$%.6f", x)
}

// FormatFullBlock returns multi-line text for /cost output.
// fromBuiltInTable is false when rates come from config (cost_per_mtok).
func FormatFullBlock(u Usage, model string, inPerMTok, outPerMTok float64, fromBuiltInTable bool) string {
	if !u.HasAny() {
		return "Session token usage:\n  (no API usage data yet — usage appears after model calls that return token counts)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session token usage:\n")
	fmt.Fprintf(&b, "  prompt (input):      %d tokens\n", u.PromptTokens)
	fmt.Fprintf(&b, "  completion (output): %d tokens\n", u.CompletionTokens)
	if u.TotalTokens > 0 {
		fmt.Fprintf(&b, "  total:                 %d tokens\n", u.TotalTokens)
	}
	est := (float64(u.PromptTokens)/1e6)*inPerMTok + (float64(u.CompletionTokens)/1e6)*outPerMTok
	switch {
	case !fromBuiltInTable:
		fmt.Fprintf(&b, "  est. cost:             %s (from cost_per_mtok: $%.2f / $%.2f per MTok)\n",
			FormatUSD(est), inPerMTok, outPerMTok)
	case inPerMTok > 0 || outPerMTok > 0:
		fmt.Fprintf(&b, "  est. cost:             %s (%s @ $%.2f / $%.2f per MTok)\n",
			FormatUSD(est), model, inPerMTok, outPerMTok)
	default:
		fmt.Fprintf(&b, "  est. cost:             %s (no built-in pricing for %q — set cost_per_mtok)\n",
			FormatUSD(est), model)
	}
	return b.String()
}
