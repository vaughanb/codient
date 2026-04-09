// Package agentlog writes newline-delimited JSON events for operator observability.
package agentlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Logger writes one JSON object per line. Safe for concurrent use.
type Logger struct {
	w  io.Writer
	mu sync.Mutex
}

// New creates a logger; if w is nil, returns nil (no-op).
func New(w io.Writer) *Logger {
	if w == nil {
		return nil
	}
	return &Logger{w: w}
}

// NewFile opens path for append (create if needed).
func NewFile(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{w: f}, nil
}

func (l *Logger) emit(v map[string]any) {
	if l == nil {
		return
	}
	v["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = json.NewEncoder(l.w).Encode(v)
}

// LLM records a chat completion round (after the HTTP call).
func (l *Logger) LLM(round int, model string, duration time.Duration, err error, numChoices int) {
	m := map[string]any{
		"type":        "llm",
		"round":       round,
		"model":       model,
		"duration_ms": duration.Milliseconds(),
		"choices":     numChoices,
	}
	if err != nil {
		m["error"] = err.Error()
	}
	l.emit(m)
}

// ToolStart records invocation (arguments summarized).
func (l *Logger) ToolStart(name string, argSummary map[string]any) {
	m := map[string]any{
		"type": "tool_start",
		"tool": name,
	}
	for k, v := range argSummary {
		m[k] = v
	}
	l.emit(m)
}

// ToolEnd records completion.
func (l *Logger) ToolEnd(name string, duration time.Duration, err error, summary map[string]any) {
	m := map[string]any{
		"type":        "tool_end",
		"tool":        name,
		"duration_ms": duration.Milliseconds(),
	}
	if err != nil {
		m["error"] = err.Error()
	}
	for k, v := range summary {
		m[k] = v
	}
	l.emit(m)
}

// SummarizeArgs produces a small JSON-friendly summary of tool arguments (no huge payloads).
func SummarizeArgs(name string, argsJSON []byte) map[string]any {
	m := map[string]any{
		"arg_bytes": len(argsJSON),
	}
	if len(argsJSON) == 0 {
		return m
	}
	h := sha256.Sum256(argsJSON)
	m["arg_sha256"] = hex.EncodeToString(h[:8])
	var raw map[string]any
	if json.Unmarshal(argsJSON, &raw) != nil {
		return m
	}
	switch name {
	case "run_command":
		if argv, ok := raw["argv"].([]any); ok {
			ss := make([]string, 0, len(argv))
			for _, a := range argv {
				if s, ok := a.(string); ok {
					ss = append(ss, s)
				}
			}
			m["argv"] = ss
		}
		if cwd, ok := raw["cwd"].(string); ok {
			m["cwd"] = cwd
		}
	case "run_shell":
		if cmd, ok := raw["command"].(string); ok {
			m["command_len"] = len(cmd)
			if len(cmd) > 120 {
				m["command_prefix"] = cmd[:120]
			} else {
				m["command_prefix"] = cmd
			}
		}
		if cwd, ok := raw["cwd"].(string); ok {
			m["cwd"] = cwd
		}
	case "ensure_dir":
		if p, ok := raw["path"].(string); ok {
			m["path"] = p
		}
	case "read_file", "path_stat", "remove_path":
		if p, ok := raw["path"].(string); ok {
			m["path"] = p
		}
	case "move_path", "copy_path":
		if p, ok := raw["from"].(string); ok {
			m["from"] = p
		}
		if p, ok := raw["to"].(string); ok {
			m["to"] = p
		}
	case "glob_files":
		if p, ok := raw["under"].(string); ok {
			m["under"] = p
		}
		if p, ok := raw["pattern"].(string); ok {
			m["pattern"] = p
		}
	case "fetch_url":
		if u, ok := raw["url"].(string); ok {
			m["url"] = u
		}
	case "web_search":
		if q, ok := raw["query"].(string); ok {
			m["query"] = q
		}
	case "write_file":
		if p, ok := raw["path"].(string); ok {
			m["path"] = p
		}
		if c, ok := raw["content"].(string); ok {
			m["content_len"] = len(c)
		}
	case "str_replace":
		if p, ok := raw["path"].(string); ok {
			m["path"] = p
		}
		if old, ok := raw["old_string"].(string); ok {
			m["old_string_len"] = len(old)
		}
		if nw, ok := raw["new_string"].(string); ok {
			m["new_string_len"] = len(nw)
		}
	case "patch_file":
		if p, ok := raw["path"].(string); ok {
			m["path"] = p
		}
		if d, ok := raw["diff"].(string); ok {
			m["diff_len"] = len(d)
		}
	case "insert_lines":
		if p, ok := raw["path"].(string); ok {
			m["path"] = p
		}
		if c, ok := raw["content"].(string); ok {
			m["content_len"] = len(c)
		}
	case "grep":
		if p, ok := raw["pattern"].(string); ok {
			m["pattern"] = p
		}
		if pre, ok := raw["path_prefix"].(string); ok {
			m["path_prefix"] = pre
		}
	case "list_dir":
		if p, ok := raw["path"].(string); ok {
			m["path"] = p
		}
		if d, ok := raw["max_depth"].(float64); ok {
			m["max_depth"] = int(d)
		}
	case "search_files":
		if u, ok := raw["under"].(string); ok {
			m["under"] = u
		}
		if s, ok := raw["substring"].(string); ok {
			m["substring"] = s
		}
		if s, ok := raw["suffix"].(string); ok {
			m["suffix"] = s
		}
	case "echo":
		if msg, ok := raw["message"].(string); ok {
			r := []rune(msg)
			if len(r) > 80 {
				m["message"] = string(r[:80]) + "…"
			} else {
				m["message"] = msg
			}
		}
	}
	return m
}
