// Package config loads settings from a persistent config file (~/.codient/config.json).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultBaseURL          = "http://127.0.0.1:1234/v1"
	defaultAPIKey           = "codient"
	defaultMaxConcurrent    = 3
	defaultExecTimeoutSec   = 120
	defaultExecMaxOutBytes  = 256 * 1024
	maxExecTimeoutSec       = 3600
	maxExecMaxOutputBytes   = 10 * 1024 * 1024
	defaultContextReserve   = 4096
	defaultMaxLLMRetries    = 2
	defaultFetchMaxBytes    = 1024 * 1024
	maxFetchMaxBytes        = 10 * 1024 * 1024
	defaultFetchTimeoutSec  = 30
	maxFetchTimeoutSec      = 300
	defaultAutoCompactPct   = 75
	defaultSearchMaxResults = 5
	maxSearchMaxResults     = 10
	// MaxFetchWebRatePerSec and MaxFetchWebRateBurst cap persisted network rate limits (fetch_url + web_search).
	MaxFetchWebRatePerSec = 100
	MaxFetchWebRateBurst  = 50
)

// ModelProfile holds per-mode LLM connection overrides.
// Any non-empty field overrides the corresponding top-level Config default.
type ModelProfile struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Config holds runtime settings.
type Config struct {
	BaseURL       string
	APIKey        string
	Model         string
	MaxConcurrent int
	// Models holds optional per-mode overrides keyed by mode name ("plan", "build", "ask").
	// Fields left empty inherit from the top-level BaseURL/APIKey/Model.
	Models map[string]*ModelProfile
	// Workspace is the root directory for coding tools (read_file, list_dir, search_files, write_file).
	Workspace string
	// ExecAllowlist is a list of lowercase command names (first argv) permitted for run_command and run_shell.
	// When unset, defaults to go, git, and the platform shell (cmd or sh); set exec_disable to disable.
	ExecAllowlist []string
	// ExecTimeoutSeconds caps each run_command (default 120, max 3600).
	ExecTimeoutSeconds int
	// ExecMaxOutputBytes truncates combined stdout+stderr (default 256KiB, max 10MiB).
	ExecMaxOutputBytes int
	// ContextWindowTokens is the model's context window in tokens (0 = no limit).
	ContextWindowTokens int
	// ContextReserveTokens is headroom reserved for the model's reply (default 4096).
	ContextReserveTokens int
	// MaxLLMRetries is the number of retries for transient LLM errors (default 2).
	MaxLLMRetries int
	// StreamWithTools enables SSE token streaming for chat requests that include tools.
	// Default false: many local OpenAI-compatible servers omit or mishandle tool_calls in streamed responses.
	StreamWithTools bool
	// FetchAllowHosts lists hostnames allowed for fetch_url from ~/.codient/config.json.
	// Subdomains match. Empty base list still allows fetch_url in interactive REPL when the
	// user can approve unknown hosts, and/or via FetchPreapproved.
	FetchAllowHosts []string
	// FetchPreapproved enables the built-in documentation/code-domain host preset (default true).
	FetchPreapproved bool
	// FetchMaxBytes caps fetch_url response bodies (default 1MiB, max 10MiB).
	FetchMaxBytes int
	// FetchTimeoutSec caps each fetch_url request (default 30, max 300).
	FetchTimeoutSec int
	// SearchBaseURL is the SearXNG base URL for the web_search tool (e.g. "http://localhost:8080").
	// Empty means web_search is not registered.
	SearchBaseURL string
	// SearchMaxResults caps results per web_search query (default 5, max 10).
	SearchMaxResults int
	// FetchWebRatePerSec limits combined fetch_url and web_search requests (token bucket). 0 = disabled.
	FetchWebRatePerSec int
	// FetchWebRateBurst is the bucket size for FetchWebRatePerSec. If 0 while rate is set, defaults to rate per second.
	FetchWebRateBurst int
	// AutoCompactPct is the context usage percentage (0-100) that triggers automatic
	// compaction (LLM-summarize) between turns. 0 disables. Default 75.
	AutoCompactPct int
	// AutoCheckCmd is the shell command to run after file-editing tools.
	// Empty triggers auto-detection from workspace markers (go.mod, package.json, etc.).
	// Set to "off" to disable.
	AutoCheckCmd string

	// Mode is the default mode from config (build|ask|plan). Applied in main before CLI flag override.
	Mode string
	// Plain disables markdown/ANSI output.
	Plain bool
	// Quiet suppresses the welcome banner.
	Quiet bool
	// Verbose enables extra diagnostics.
	Verbose bool
	// LogPath is the default JSONL log path (overridden by -log flag).
	LogPath string
	// StreamReply controls assistant token streaming (nil pointer in PersistentConfig = default true).
	StreamReply bool
	// Progress forces progress output on stderr.
	Progress bool
	// DesignSaveDir overrides the directory for saved implementation plans.
	DesignSaveDir string
	// DesignSave controls whether plan-mode plans are saved to disk (default true).
	DesignSave bool
	// ProjectContext opt-out: "off" to disable auto-detected project hints.
	ProjectContext string
	// AstGrep is the resolved ast-grep binary path, empty if unavailable, or "off" to disable.
	AstGrep string
}

// Load reads configuration from the persistent config file.
// All settings come from ~/.codient/config.json with built-in defaults.
// CLI flags override config values (handled by the caller via flag.Visit).
func Load() (*Config, error) {
	pc, err := LoadPersistentConfig()
	if err != nil {
		pc = &PersistentConfig{}
	}

	baseURL := strings.TrimSpace(pc.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	apiKey := strings.TrimSpace(pc.APIKey)
	if apiKey == "" {
		apiKey = defaultAPIKey
	}
	model := strings.TrimSpace(pc.Model)

	ws := strings.TrimSpace(os.Getenv("CODIENT_WORKSPACE"))
	if ws == "" {
		ws = strings.TrimSpace(pc.Workspace)
	}
	if ws == "" {
		if wd, err := os.Getwd(); err == nil {
			if abs, err := filepath.Abs(wd); err == nil {
				ws = abs
			} else {
				ws = wd
			}
		}
	}

	execAllowlist := parseExecAllowlist(pc.ExecAllowlist)
	if pc.ExecDisable {
		execAllowlist = nil
	} else if len(execAllowlist) == 0 {
		execAllowlist = defaultExecAllowlist()
	}

	fetchHosts := parseFetchAllowHosts(pc.FetchAllowHosts)
	fetchPreapproved := true
	if pc.FetchPreapproved != nil {
		fetchPreapproved = *pc.FetchPreapproved
	}

	maxConcurrent := pc.MaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = defaultMaxConcurrent
	}

	execTimeout := pc.ExecTimeoutSec
	if execTimeout == 0 {
		execTimeout = defaultExecTimeoutSec
	}
	execMaxOut := pc.ExecMaxOutBytes
	if execMaxOut == 0 {
		execMaxOut = defaultExecMaxOutBytes
	}

	contextReserve := pc.ContextReserve
	if contextReserve == 0 {
		contextReserve = defaultContextReserve
	}

	maxLLMRetries := pc.MaxLLMRetries
	if maxLLMRetries == 0 {
		maxLLMRetries = defaultMaxLLMRetries
	}

	fetchMax := pc.FetchMaxBytes
	if fetchMax == 0 {
		fetchMax = defaultFetchMaxBytes
	}
	fetchTimeout := pc.FetchTimeoutSec
	if fetchTimeout == 0 {
		fetchTimeout = defaultFetchTimeoutSec
	}

	searchMaxResults := pc.SearchMaxResults
	if searchMaxResults == 0 {
		searchMaxResults = defaultSearchMaxResults
	}

	fetchWebRate := pc.FetchWebRatePerSec
	if fetchWebRate < 0 {
		fetchWebRate = 0
	}
	if fetchWebRate > MaxFetchWebRatePerSec {
		fetchWebRate = MaxFetchWebRatePerSec
	}
	fetchWebBurst := pc.FetchWebRateBurst
	if fetchWebBurst < 0 {
		fetchWebBurst = 0
	}
	if fetchWebRate > 0 && fetchWebBurst == 0 {
		fetchWebBurst = fetchWebRate
	}
	if fetchWebBurst > MaxFetchWebRateBurst {
		fetchWebBurst = MaxFetchWebRateBurst
	}

	autoCompactPct := pc.AutoCompactPct
	if autoCompactPct == 0 && pc.AutoCompactPct == 0 {
		autoCompactPct = defaultAutoCompactPct
	}

	streamReply := true
	if pc.StreamReply != nil {
		streamReply = *pc.StreamReply
	}
	designSave := true
	if pc.DesignSave != nil {
		designSave = *pc.DesignSave
	}

	models := loadModelProfiles(pc.Models)

	c := &Config{
		BaseURL:              baseURL,
		APIKey:               apiKey,
		Model:                model,
		MaxConcurrent:        maxConcurrent,
		Models:               models,
		Workspace:            ws,
		ExecAllowlist:        execAllowlist,
		ExecTimeoutSeconds:   execTimeout,
		ExecMaxOutputBytes:   execMaxOut,
		ContextWindowTokens:  pc.ContextWindow,
		ContextReserveTokens: contextReserve,
		MaxLLMRetries:        maxLLMRetries,
		StreamWithTools:      pc.StreamWithTools,
		FetchAllowHosts:      fetchHosts,
		FetchPreapproved:     fetchPreapproved,
		FetchMaxBytes:        fetchMax,
		FetchTimeoutSec:      fetchTimeout,
		SearchBaseURL:        strings.TrimSpace(pc.SearchBaseURL),
		SearchMaxResults:     searchMaxResults,
		FetchWebRatePerSec:   fetchWebRate,
		FetchWebRateBurst:    fetchWebBurst,
		AutoCompactPct:       autoCompactPct,
		AutoCheckCmd:         strings.TrimSpace(pc.AutoCheckCmd),
		Mode:                 strings.TrimSpace(pc.Mode),
		Plain:                pc.Plain,
		Quiet:                pc.Quiet,
		Verbose:              pc.Verbose,
		LogPath:              strings.TrimSpace(pc.LogPath),
		StreamReply:          streamReply,
		Progress:             pc.Progress,
		DesignSaveDir:        strings.TrimSpace(pc.DesignSaveDir),
		DesignSave:           designSave,
		ProjectContext:       strings.TrimSpace(pc.ProjectContext),
		AstGrep:              strings.TrimSpace(pc.AstGrep),
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.ExecTimeoutSeconds < 1 {
		c.ExecTimeoutSeconds = defaultExecTimeoutSec
	}
	if c.ExecTimeoutSeconds > maxExecTimeoutSec {
		c.ExecTimeoutSeconds = maxExecTimeoutSec
	}
	if c.ExecMaxOutputBytes < 1 {
		c.ExecMaxOutputBytes = defaultExecMaxOutBytes
	}
	if c.ExecMaxOutputBytes > maxExecMaxOutputBytes {
		c.ExecMaxOutputBytes = maxExecMaxOutputBytes
	}
	if c.MaxConcurrent < 1 {
		return nil, fmt.Errorf("max_concurrent must be at least 1")
	}
	if c.ContextWindowTokens < 0 {
		c.ContextWindowTokens = 0
	}
	if c.ContextReserveTokens < 0 {
		c.ContextReserveTokens = defaultContextReserve
	}
	if c.MaxLLMRetries < 0 {
		c.MaxLLMRetries = 0
	}
	if c.FetchMaxBytes < 1 {
		c.FetchMaxBytes = defaultFetchMaxBytes
	}
	if c.FetchMaxBytes > maxFetchMaxBytes {
		c.FetchMaxBytes = maxFetchMaxBytes
	}
	if c.FetchTimeoutSec < 1 {
		c.FetchTimeoutSec = defaultFetchTimeoutSec
	}
	if c.FetchTimeoutSec > maxFetchTimeoutSec {
		c.FetchTimeoutSec = maxFetchTimeoutSec
	}
	if c.SearchMaxResults < 1 {
		c.SearchMaxResults = defaultSearchMaxResults
	}
	if c.SearchMaxResults > maxSearchMaxResults {
		c.SearchMaxResults = maxSearchMaxResults
	}
	if c.AutoCompactPct < 0 {
		c.AutoCompactPct = 0
	}
	if c.AutoCompactPct > 100 {
		c.AutoCompactPct = 100
	}
	return c, nil
}

// RequireModel returns an error if no default model is configured (top-level model field).
func (c *Config) RequireModel() error {
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("no model configured — use /config model <name> to set one (use -list-models to see available ids)")
	}
	return nil
}

// RequireModelForMode returns an error if the effective model for the given mode
// (build, ask, or plan) is empty after applying per-mode overrides.
func (c *Config) RequireModelForMode(mode string) error {
	if strings.TrimSpace(c.EffectiveModel(mode)) == "" {
		return fmt.Errorf("no model configured for %s mode — set model or %s_model (see /config)", mode, mode)
	}
	return nil
}

// HasAnyEffectiveModel reports whether at least one of build, ask, or plan has a
// non-empty effective model (useful before running the first-time setup wizard).
func (c *Config) HasAnyEffectiveModel() bool {
	for _, m := range []string{"build", "ask", "plan"} {
		if strings.TrimSpace(c.EffectiveModel(m)) != "" {
			return true
		}
	}
	return false
}

// EffectiveWorkspace returns the resolved workspace directory (defaults to cwd at startup when unset).
func (c *Config) EffectiveWorkspace() string {
	return strings.TrimSpace(c.Workspace)
}

// EffectiveModelConfig resolves the (baseURL, apiKey, model) triple for a given mode.
// Per-mode overrides take precedence; unset fields fall back to the top-level defaults.
func (c *Config) EffectiveModelConfig(mode string) (baseURL, apiKey, model string) {
	baseURL, apiKey, model = c.BaseURL, c.APIKey, c.Model
	if mp := c.Models[mode]; mp != nil {
		if mp.BaseURL != "" {
			baseURL = mp.BaseURL
		}
		if mp.APIKey != "" {
			apiKey = mp.APIKey
		}
		if mp.Model != "" {
			model = mp.Model
		}
	}
	return baseURL, apiKey, model
}

// EffectiveModel is a convenience that returns only the resolved model name for a mode.
func (c *Config) EffectiveModel(mode string) string {
	_, _, model := c.EffectiveModelConfig(mode)
	return model
}

// HasModeOverrides returns true if any per-mode model profiles are configured.
func (c *Config) HasModeOverrides() bool {
	for _, mp := range c.Models {
		if mp != nil && (mp.BaseURL != "" || mp.APIKey != "" || mp.Model != "") {
			return true
		}
	}
	return false
}

// defaultExecAllowlist is used when exec_allowlist is unset and exec_disable is not set,
// so run_command / run_shell are registered without extra configuration.
// It includes the platform shell (cmd on Windows, sh on Unix) so run_shell can run mkdir and other builtins.
func defaultExecAllowlist() []string {
	if runtime.GOOS == "windows" {
		return []string{"go", "git", "cmd"}
	}
	return []string{"go", "git", "sh"}
}

// ParseExecAllowlistString parses a comma-separated exec allowlist string.
// Entries are lowercased; ".exe" is stripped for comparison on any OS.
func ParseExecAllowlistString(s string) []string {
	return parseExecAllowlist(s)
}

// ParseFetchAllowHostsString parses a comma-separated fetch allow hosts string.
func ParseFetchAllowHostsString(s string) []string {
	return parseFetchAllowHosts(s)
}

func parseExecAllowlist(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, part := range strings.Split(s, ",") {
		name := strings.TrimSpace(strings.ToLower(part))
		if name == "" {
			continue
		}
		name = strings.TrimSuffix(name, ".exe")
		name = strings.TrimSuffix(name, ".bat")
		name = strings.TrimSuffix(name, ".cmd")
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func loadModelProfiles(pm map[string]*PersistentModelProfile) map[string]*ModelProfile {
	if len(pm) == 0 {
		return nil
	}
	out := make(map[string]*ModelProfile, len(pm))
	for mode, p := range pm {
		if p == nil {
			continue
		}
		mp := &ModelProfile{
			BaseURL: strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"),
			APIKey:  strings.TrimSpace(p.APIKey),
			Model:   strings.TrimSpace(p.Model),
		}
		if mp.BaseURL != "" || mp.APIKey != "" || mp.Model != "" {
			out[mode] = mp
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeFetchAllowHostSlices(a, b []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, list := range [][]string{a, b} {
		for _, h := range list {
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			out = append(out, h)
		}
	}
	return out
}

// parseFetchAllowHosts parses fetch_allow_hosts (comma-separated hostnames, no schemes or paths).
func parseFetchAllowHosts(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, part := range strings.Split(s, ",") {
		h := strings.ToLower(strings.TrimSpace(part))
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}
