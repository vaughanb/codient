package agent

import (
	"fmt"
	"strings"
	"time"

	"codient/internal/agentlog"
)

// formatProgressDur renders a duration for stderr progress (e.g. 420ms, 4.8s).
func formatProgressDur(d time.Duration) string {
	d = d.Round(time.Millisecond)
	if d < time.Second {
		ms := d.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		return fmt.Sprintf("%dms", ms)
	}
	s := d.Seconds()
	if s < 60 {
		if s < 10 {
			return fmt.Sprintf("%.2fs", s)
		}
		return fmt.Sprintf("%.1fs", s)
	}
	return d.Round(time.Second).String()
}

// ProgressToolCompact is a short label for progress (no path= prefixes).
func ProgressToolCompact(toolName string, argsJSON []byte) string {
	sum := agentlog.SummarizeArgs(toolName, argsJSON)
	switch toolName {
	case "read_file", "write_file":
		if p, ok := sum["path"].(string); ok && p != "" {
			return toolName + " " + p
		}
		return toolName
	case "list_dir":
		if p, ok := sum["path"].(string); ok && strings.TrimSpace(p) != "" && p != "." {
			return "list_dir " + p
		}
		return "list_dir"
	case "grep":
		if p, ok := sum["pattern"].(string); ok && p != "" {
			return "grep " + truncateRunes(p, 40)
		}
		return "grep"
	case "search_files":
		var bits []string
		if v, ok := sum["substring"]; ok && fmt.Sprint(v) != "" {
			bits = append(bits, fmt.Sprint(v))
		}
		if v, ok := sum["suffix"]; ok && fmt.Sprint(v) != "" {
			bits = append(bits, fmt.Sprint(v))
		}
		if len(bits) == 0 {
			return "search_files"
		}
		return "search_files " + strings.Join(bits, " ")
	case "run_command":
		if argv, ok := sum["argv"].([]string); ok && len(argv) > 0 {
			s := strings.Join(argv, " ")
			if len(s) > 50 {
				s = s[:50] + "…"
			}
			return "run " + s
		}
		return "run_command"
	case "get_time":
		return "get_time"
	case "echo":
		if msg, ok := sum["message"].(string); ok && msg != "" {
			return "echo " + truncateRunes(msg, 36)
		}
		return "echo"
	default:
		return ProgressToolLine(toolName, argsJSON)
	}
}

func progressErrShort(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	s = strings.TrimSpace(strings.Split(s, "\n")[0])
	if len(s) > 72 {
		return s[:72] + "…"
	}
	return s
}
