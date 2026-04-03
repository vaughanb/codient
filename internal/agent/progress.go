package agent

import (
	"fmt"
	"strings"

	"codient/internal/agentlog"
)

// progressArgsHint turns SummarizeArgs output into a short human line (no raw content).
func progressArgsHint(name string, sum map[string]any) string {
	if sum == nil {
		return ""
	}
	switch name {
	case "run_command":
		if argv, ok := sum["argv"].([]string); ok && len(argv) > 0 {
			return fmt.Sprintf("%q", strings.Join(argv, " "))
		}
	case "read_file", "write_file":
		if p, ok := sum["path"].(string); ok && p != "" {
			s := "path=" + p
			if name == "write_file" {
				if n, ok := sum["content_len"].(int); ok {
					s += fmt.Sprintf(" content_len=%d", n)
				}
			}
			return s
		}
	case "grep":
		var parts []string
		if p, ok := sum["pattern"].(string); ok && p != "" {
			parts = append(parts, "pattern="+truncateRunes(p, 60))
		}
		if pre, ok := sum["path_prefix"].(string); ok && pre != "" {
			parts = append(parts, "under="+pre)
		}
		return strings.Join(parts, " ")
	case "list_dir":
		if p, ok := sum["path"].(string); ok {
			s := "path=" + p
			if d, ok := sum["max_depth"].(int); ok {
				s += fmt.Sprintf(" max_depth=%d", d)
			}
			return s
		}
	case "search_files":
		var parts []string
		for _, k := range []string{"under", "substring", "suffix"} {
			if v, ok := sum[k]; ok && fmt.Sprint(v) != "" {
				parts = append(parts, fmt.Sprintf("%s=%v", k, v))
			}
		}
		return strings.Join(parts, " ")
	}
	// Fallback: omit noisy keys
	var parts []string
	for _, k := range []string{"path", "under", "substring", "suffix", "message"} {
		if v, ok := sum[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// ProgressToolLine builds the "before tool" stderr line using redacted summaries.
func ProgressToolLine(toolName string, argsJSON []byte) string {
	sum := agentlog.SummarizeArgs(toolName, argsJSON)
	hint := progressArgsHint(toolName, sum)
	if hint == "" {
		return toolName
	}
	return toolName + " " + hint
}
