package sessionstore

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ResumeSummaryLine builds a one-line summary for stderr when resuming a REPL session.
// sessionID may be empty. msgs are raw JSON messages as stored on disk.
func ResumeSummaryLine(sessionID string, msgs []json.RawMessage) string {
	n := countUserMessages(msgs)
	if n == 0 {
		return ""
	}
	preview := lastUserPreview(msgs, 100)
	var parts []string
	if tid := strings.TrimSpace(sessionID); tid != "" {
		parts = append(parts, "session "+truncateRunes(tid, 56))
	}
	if n == 1 {
		parts = append(parts, "1 turn")
	} else if n > 1 {
		parts = append(parts, fmt.Sprintf("%d turns", n))
	}
	if preview != "" {
		parts = append(parts, "last: "+preview)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func lastUserPreview(msgs []json.RawMessage, maxRunes int) string {
	if s := lastUserPreviewFiltered(msgs, maxRunes, true); s != "" {
		return s
	}
	return lastUserPreviewFiltered(msgs, maxRunes, false)
}

func lastUserPreviewFiltered(msgs []json.RawMessage, maxRunes int, skipInternal bool) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if MessageRole(msgs[i]) != "user" {
			continue
		}
		c := strings.TrimSpace(MessageContent(msgs[i]))
		if c == "" {
			continue
		}
		line := strings.Split(c, "\n")[0]
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.Join(strings.Fields(line), " ")
		if skipInternal && isInternalResumePreviewLine(line) {
			continue
		}
		return truncateRunes(line, maxRunes)
	}
	return ""
}

func isInternalResumePreviewLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	for _, p := range []string{
		"[Mode switched from ",
		"This session is already in build mode. Available tools:",
		"You just provided suggestions. Before I accept them, try to DISPROVE each one using tool calls:",
	} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	rs := []rune(s)
	if len(rs) > max {
		rs = rs[:max]
	}
	return strings.TrimSpace(string(rs)) + "…"
}
