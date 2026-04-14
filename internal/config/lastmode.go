package config

import (
	"os"
	"path/filepath"
	"strings"
)

// normalizeReplMode mirrors prompt.ParseMode without importing prompt (avoids config↔prompt cycle).
func normalizeReplMode(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "build":
		return "build", true
	case "ask":
		return "ask", true
	case "plan", "design":
		return "plan", true
	default:
		return "", false
	}
}

// LoadLastMode reads the mode persisted from the last REPL run (~/.codient/last_mode).
// Returns empty when missing or invalid so callers fall back to config / build.
func LoadLastMode() string {
	path, err := lastModePath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	m, ok := normalizeReplMode(s)
	if !ok {
		return ""
	}
	return m
}

// SaveLastMode persists the current REPL mode for the next process start.
// Invalid values are ignored. Errors are silent (best-effort).
func SaveLastMode(mode string) {
	m, ok := normalizeReplMode(mode)
	if !ok {
		return
	}
	path, err := lastModePath()
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	line := m + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(line), 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

func lastModePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last_mode"), nil
}
