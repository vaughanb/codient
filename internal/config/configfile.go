package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const currentSchemaVersion = 1

// PersistentConfig holds all user-configurable settings saved to ~/.codient/config.json.
type PersistentConfig struct {
	// Schema version for migration support.
	SchemaVersion int `json:"schema_version,omitempty"`

	// Connection
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Model   string `json:"model,omitempty"`

	// Default mode
	Mode string `json:"mode,omitempty"` // build|ask|plan

	// Workspace
	Workspace string `json:"workspace,omitempty"`

	// Agent limits
	MaxConcurrent int `json:"max_concurrent,omitempty"`

	// Exec
	ExecAllowlist   string `json:"exec_allowlist,omitempty"`
	ExecDisable     bool   `json:"exec_disable,omitempty"`
	ExecTimeoutSec  int    `json:"exec_timeout_sec,omitempty"`
	ExecMaxOutBytes int    `json:"exec_max_output_bytes,omitempty"`

	// Context
	ContextWindow  int `json:"context_window,omitempty"`
	ContextReserve int `json:"context_reserve,omitempty"`

	// LLM
	MaxLLMRetries   int  `json:"max_llm_retries,omitempty"`
	StreamWithTools bool `json:"stream_with_tools,omitempty"`

	// Fetch
	FetchAllowHosts  string `json:"fetch_allow_hosts,omitempty"`
	FetchPreapproved *bool  `json:"fetch_preapproved,omitempty"`
	FetchMaxBytes    int    `json:"fetch_max_bytes,omitempty"`
	FetchTimeoutSec  int    `json:"fetch_timeout_sec,omitempty"`
	// FetchWebRatePerSec limits combined fetch_url + web_search (0 = off).
	FetchWebRatePerSec int `json:"fetch_web_rate_per_sec,omitempty"`
	FetchWebRateBurst  int `json:"fetch_web_rate_burst,omitempty"`

	// Search
	SearchBaseURL    string `json:"search_url,omitempty"`
	SearchMaxResults int    `json:"search_max_results,omitempty"`

	// Auto
	AutoCompactPct int    `json:"autocompact_threshold,omitempty"`
	AutoCheckCmd   string `json:"autocheck_cmd,omitempty"`

	// UI/Output
	Plain   bool `json:"plain,omitempty"`
	Quiet   bool `json:"quiet,omitempty"`
	Verbose bool `json:"verbose,omitempty"`

	// Logging
	LogPath string `json:"log,omitempty"`

	// Streaming
	StreamReply *bool `json:"stream_reply,omitempty"`
	Progress    bool  `json:"progress,omitempty"`

	// Plan save
	DesignSaveDir string `json:"design_save_dir,omitempty"`
	DesignSave    *bool  `json:"design_save,omitempty"`

	// Project
	ProjectContext string `json:"project_context,omitempty"`

	// ast-grep: "auto" (default), "off", or explicit path to binary
	AstGrep string `json:"ast_grep,omitempty"`

	// Per-mode model overrides (keyed by "plan", "build", "ask").
	Models map[string]*PersistentModelProfile `json:"models,omitempty"`
}

// PersistentModelProfile holds per-mode LLM connection overrides in config.json.
type PersistentModelProfile struct {
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Model   string `json:"model,omitempty"`
}

func stateDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv("CODIENT_STATE_DIR")); d != "" {
		return filepath.Abs(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codient"), nil
}

func configFilePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// migrateConfig migrates a config from an older schema version to the current version.
// Returns an error if the config is from a newer version than this binary supports.
func migrateConfig(pc *PersistentConfig) error {
	if pc.SchemaVersion > currentSchemaVersion {
		return fmt.Errorf("config file is from a newer version of codient (schema version %d); this binary supports up to version %d — please upgrade codient", pc.SchemaVersion, currentSchemaVersion)
	}
	// Version 0 → 1: no structural changes, just add version field.
	if pc.SchemaVersion == 0 {
		pc.SchemaVersion = 1
	}
	return nil
}

// LoadPersistentConfig reads ~/.codient/config.json.
// Returns a zero-value struct (not an error) if the file does not exist.
func LoadPersistentConfig() (*PersistentConfig, error) {
	path, err := configFilePath()
	if err != nil {
		return &PersistentConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PersistentConfig{}, nil
		}
		return nil, err
	}
	var pc PersistentConfig
	if err := json.Unmarshal(data, &pc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := migrateConfig(&pc); err != nil {
		return nil, err
	}
	return &pc, nil
}

// SavePersistentConfig writes the config atomically to ~/.codient/config.json.
func SavePersistentConfig(pc *PersistentConfig) error {
	// Always stamp the current schema version.
	pc.SchemaVersion = currentSchemaVersion

	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config state dir: %w", err)
	}
	path, err := configFilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ConfigToPersistent builds a PersistentConfig from the runtime Config for saving.
func ConfigToPersistent(cfg *Config) *PersistentConfig {
	pc := &PersistentConfig{
		BaseURL:            cfg.BaseURL,
		APIKey:             cfg.APIKey,
		Model:              cfg.Model,
		Mode:               cfg.Mode,
		Workspace:          cfg.Workspace,
		MaxConcurrent:      cfg.MaxConcurrent,
		ExecAllowlist:      strings.Join(cfg.ExecAllowlist, ","),
		ExecTimeoutSec:     cfg.ExecTimeoutSeconds,
		ExecMaxOutBytes:    cfg.ExecMaxOutputBytes,
		ContextWindow:      cfg.ContextWindowTokens,
		ContextReserve:     cfg.ContextReserveTokens,
		MaxLLMRetries:      cfg.MaxLLMRetries,
		StreamWithTools:    cfg.StreamWithTools,
		FetchAllowHosts:    strings.Join(cfg.FetchAllowHosts, ","),
		FetchMaxBytes:      cfg.FetchMaxBytes,
		FetchTimeoutSec:    cfg.FetchTimeoutSec,
		FetchWebRatePerSec: cfg.FetchWebRatePerSec,
		FetchWebRateBurst:  cfg.FetchWebRateBurst,
		SearchBaseURL:      cfg.SearchBaseURL,
		SearchMaxResults:   cfg.SearchMaxResults,
		AutoCompactPct:     cfg.AutoCompactPct,
		AutoCheckCmd:       cfg.AutoCheckCmd,
		Plain:              cfg.Plain,
		Quiet:              cfg.Quiet,
		Verbose:            cfg.Verbose,
		LogPath:            cfg.LogPath,
		Progress:           cfg.Progress,
		DesignSaveDir:      cfg.DesignSaveDir,
		ProjectContext:     cfg.ProjectContext,
		AstGrep:            cfg.AstGrep,
	}
	if len(cfg.Models) > 0 {
		pc.Models = make(map[string]*PersistentModelProfile, len(cfg.Models))
		for mode, mp := range cfg.Models {
			if mp == nil {
				continue
			}
			pmp := &PersistentModelProfile{
				BaseURL: mp.BaseURL,
				APIKey:  mp.APIKey,
				Model:   mp.Model,
			}
			if pmp.BaseURL != "" || pmp.APIKey != "" || pmp.Model != "" {
				pc.Models[mode] = pmp
			}
		}
		if len(pc.Models) == 0 {
			pc.Models = nil
		}
	}
	if !cfg.FetchPreapproved {
		f := false
		pc.FetchPreapproved = &f
	}
	if !cfg.StreamReply {
		f := false
		pc.StreamReply = &f
	}
	if !cfg.DesignSave {
		f := false
		pc.DesignSave = &f
	}
	return pc
}

// AppendPersistentFetchHost adds host to fetch_allow_hosts in ~/.codient/config.json if not already present.
func AppendPersistentFetchHost(host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return fmt.Errorf("empty host")
	}
	pc, err := LoadPersistentConfig()
	if err != nil {
		return err
	}
	cur := parseFetchAllowHosts(pc.FetchAllowHosts)
	for _, h := range cur {
		if h == host {
			return nil
		}
	}
	cur = append(cur, host)
	pc.FetchAllowHosts = strings.Join(cur, ",")
	return SavePersistentConfig(pc)
}
