package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codient/internal/config"
)

// FileConfig is the top-level shape of hooks.json.
type FileConfig struct {
	Hooks map[string][]MatcherGroup `json:"hooks"`
}

// MatcherGroup is one matcher regex and its handlers (Codex/Claude-style nesting).
type MatcherGroup struct {
	Matcher string    `json:"matcher"`
	Hooks   []Handler `json:"hooks"`
	// SourcePath is set at load time (which file this group came from).
	SourcePath string `json:"-"`
}

// Handler is a single hook command.
type Handler struct {
	Type    string `json:"type"` // "command" (only type supported in phase 1)
	Command string `json:"command"`
	// Timeout is seconds; 0 means use defaultDefaultHookTimeoutSec.
	Timeout int `json:"timeout"`
	// TimeoutSec is accepted as an alias (Codex).
	TimeoutSec int `json:"timeoutSec"`
	// FailClosed, when true, blocks the guarded action if the hook crashes, times out, or returns invalid JSON.
	FailClosed bool `json:"failClosed"`
}

// EffectiveTimeout returns the handler timeout in seconds.
func (h Handler) EffectiveTimeout() int {
	if h.Timeout > 0 {
		return h.Timeout
	}
	if h.TimeoutSec > 0 {
		return h.TimeoutSec
	}
	return defaultHookTimeoutSec
}

// Loaded holds merged hook groups from all config layers.
type Loaded struct {
	ByEvent map[string][]MatcherGroup
	Paths   []string // hooks.json files that were read (may be empty if none exist)
}

// Load reads and merges ~/.codient/hooks.json and <workspace>/.codient/hooks.json.
// Missing files are ignored. Parse errors are returned.
func Load(workspace string) (*Loaded, error) {
	var paths []string
	var merged FileConfig

	appendFile := func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		paths = append(paths, path)
		var fc FileConfig
		if err := json.Unmarshal(data, &fc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if fc.Hooks == nil {
			return nil
		}
		for ev, groups := range fc.Hooks {
			ev = strings.TrimSpace(ev)
			if ev == "" {
				continue
			}
			for i := range groups {
				groups[i].SourcePath = path
			}
			merged.Hooks[ev] = append(merged.Hooks[ev], groups...)
		}
		return nil
	}

	if merged.Hooks == nil {
		merged.Hooks = make(map[string][]MatcherGroup)
	}

	stateDir, err := config.StateDir()
	if err == nil && stateDir != "" {
		p := filepath.Join(stateDir, "hooks.json")
		if err := appendFile(p); err != nil {
			return nil, err
		}
	}
	ws := strings.TrimSpace(workspace)
	if ws != "" {
		p := filepath.Join(ws, ".codient", "hooks.json")
		if err := appendFile(p); err != nil {
			return nil, err
		}
	}

	out := &Loaded{
		ByEvent: merged.Hooks,
		Paths:   paths,
	}
	if out.ByEvent == nil {
		out.ByEvent = make(map[string][]MatcherGroup)
	}
	return out, nil
}
