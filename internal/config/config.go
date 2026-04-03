// Package config loads settings from the environment for LM Studio and the agent.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultBaseURL         = "http://127.0.0.1:1234/v1"
	defaultAPIKey          = "lm-studio"
	defaultMaxToolSteps    = 1000
	defaultMaxConcurrent   = 3
	defaultExecTimeoutSec  = 120
	defaultExecMaxOutBytes = 256 * 1024
	maxExecTimeoutSec      = 3600
	maxExecMaxOutputBytes  = 10 * 1024 * 1024
)

// Config holds runtime settings.
type Config struct {
	BaseURL       string
	APIKey        string
	Model         string
	MaxToolSteps  int
	MaxConcurrent int
	// Workspace is the root directory for coding tools (read_file, list_dir, search_files, write_file).
	Workspace string
	// ReadFileRoot is legacy; used as workspace when Workspace is empty.
	ReadFileRoot string
	// ExecAllowlist is a list of lowercase command names (first argv) permitted for run_command (CODIENT_EXEC_ALLOWLIST).
	ExecAllowlist []string
	// ExecTimeoutSeconds caps each run_command (default 120, max 3600).
	ExecTimeoutSeconds int
	// ExecMaxOutputBytes truncates combined stdout+stderr (default 256KiB, max 10MiB).
	ExecMaxOutputBytes int
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		BaseURL:            getenv("LMSTUDIO_BASE_URL", defaultBaseURL),
		APIKey:             getenv("LMSTUDIO_API_KEY", defaultAPIKey),
		Model:              strings.TrimSpace(os.Getenv("LMSTUDIO_MODEL")),
		MaxToolSteps:       getenvInt("AGENT_MAX_TOOL_STEPS", defaultMaxToolSteps),
		MaxConcurrent:      getenvInt("LLM_MAX_CONCURRENT", defaultMaxConcurrent),
		Workspace:          strings.TrimSpace(os.Getenv("CODIENT_WORKSPACE")),
		ReadFileRoot:       strings.TrimSpace(os.Getenv("CODIENT_READ_FILE_ROOT")),
		ExecAllowlist:      parseExecAllowlist(os.Getenv("CODIENT_EXEC_ALLOWLIST")),
		ExecTimeoutSeconds: getenvInt("CODIENT_EXEC_TIMEOUT_SEC", defaultExecTimeoutSec),
		ExecMaxOutputBytes: getenvInt("CODIENT_EXEC_MAX_OUTPUT_BYTES", defaultExecMaxOutBytes),
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
	if c.MaxToolSteps < 1 {
		return nil, fmt.Errorf("AGENT_MAX_TOOL_STEPS must be at least 1")
	}
	if c.MaxConcurrent < 1 {
		return nil, fmt.Errorf("LLM_MAX_CONCURRENT must be at least 1")
	}
	return c, nil
}

// RequireModel returns an error if LMSTUDIO_MODEL is unset (needed for chat completions).
func (c *Config) RequireModel() error {
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("LMSTUDIO_MODEL is required for chat (use -list-models to see ids)")
	}
	return nil
}

// EffectiveWorkspace returns CODIENT_WORKSPACE, or if unset, CODIENT_READ_FILE_ROOT.
func (c *Config) EffectiveWorkspace() string {
	if s := strings.TrimSpace(c.Workspace); s != "" {
		return s
	}
	return strings.TrimSpace(c.ReadFileRoot)
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// parseExecAllowlist parses CODIENT_EXEC_ALLOWLIST (comma-separated names, no paths).
// Entries are lowercased; ".exe" is stripped for comparison on any OS.
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
