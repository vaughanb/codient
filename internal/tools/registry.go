// Package tools registers callable functions exposed to the LLM as OpenAI-style tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// Tool is a single function the model may invoke.
type Tool struct {
	Name        string
	Description string
	Parameters  shared.FunctionParameters
	Run         func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry maps tool names to implementations.
type Registry struct {
	mu    sync.RWMutex
	order []string
	by    map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{by: make(map[string]Tool)}
}

// Register adds a tool. It panics if name is duplicated (programmer error).
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.by[t.Name]; ok {
		panic("tools: duplicate registration: " + t.Name)
	}
	r.by[t.Name] = t
	r.order = append(r.order, t.Name)
}

// Names returns registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// OpenAITools builds the tools slice for chat completion requests.
func (r *Registry) OpenAITools() []openai.ChatCompletionToolUnionParam {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(r.order))
	for _, name := range r.order {
		t := r.by[name]
		out = append(out, openai.ChatCompletionToolUnionParam{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        t.Name,
					Description: openai.String(t.Description),
					Parameters:  t.Parameters,
				},
			},
		})
	}
	return out
}

// Run executes a tool by name. Unknown names return a structured error string for the model.
func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.mu.RLock()
	t, ok := r.by[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return t.Run(ctx, args)
}

// Default returns a registry with safe builtins and, when workspace is non-empty,
// coding tools scoped to that directory (set CODIENT_WORKSPACE or CODIENT_READ_FILE_ROOT).
// exec enables run_command when non-nil and Allowlist is non-empty (CODIENT_EXEC_ALLOWLIST).
func Default(workspace string, exec *ExecOptions) *Registry {
	r := NewRegistry()
	registerBuiltinTools(r, true)
	root := strings.TrimSpace(workspace)
	if root != "" {
		registerWorkspaceTools(r, root, exec)
	}
	return r
}

// DefaultReadOnly is like Default but omits write_file and run_command: read/search/list/grep
// only (plus echo and get_time). Use for Ask mode.
func DefaultReadOnly(workspace string) *Registry {
	r := NewRegistry()
	registerBuiltinTools(r, true)
	root := strings.TrimSpace(workspace)
	if root != "" {
		registerWorkspaceReadTools(r, root)
	}
	return r
}

// DefaultReadOnlyPlan is like DefaultReadOnly but omits echo so the model cannot substitute
// a one-line echo for a written plan. Use for Plan mode.
func DefaultReadOnlyPlan(workspace string) *Registry {
	r := NewRegistry()
	registerBuiltinTools(r, false)
	root := strings.TrimSpace(workspace)
	if root != "" {
		registerWorkspaceReadTools(r, root)
	}
	return r
}

func registerBuiltinTools(r *Registry, withEcho bool) {
	if withEcho {
		r.Register(Tool{
			Name:        "echo",
			Description: "Returns the same text back. Use to confirm the tool pipeline or repeat structured input.",
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "Text to echo back."},
				},
				"required":             []string{"message"},
				"additionalProperties": false,
			},
			Run: func(_ context.Context, args json.RawMessage) (string, error) {
				var p struct {
					Message string `json:"message"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
				return p.Message, nil
			},
		})
	}
	r.Register(Tool{
		Name:        "get_time",
		Description: "Returns the current local date and time in RFC3339 format.",
		Parameters: shared.FunctionParameters{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, _ json.RawMessage) (string, error) {
			return time.Now().Format(time.RFC3339), nil
		},
	})
}

func registerWorkspaceTools(r *Registry, root string, exec *ExecOptions) {
	registerWorkspaceReadTools(r, root)
	registerWorkspaceMutatingTools(r, root, exec)
}

func registerWorkspaceReadTools(r *Registry, root string) {
	r.Register(Tool{
		Name: "read_file",
		Description: "Reads a UTF-8 text file under the workspace root (CODIENT_WORKSPACE). " +
			"Optional max_bytes (default 262144) and 1-based start_line/end_line to return a slice of lines.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to workspace root.",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"description": "Maximum bytes to read (default 262144). Longer files are truncated.",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "1-based start line (optional). Use with end_line for a range.",
				},
				"end_line": map[string]any{
					"type":        "integer",
					"description": "1-based end line inclusive (optional).",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path      string `json:"path"`
				MaxBytes  *int64 `json:"max_bytes"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			mb := int64(defaultReadMaxBytes)
			if p.MaxBytes != nil && *p.MaxBytes > 0 {
				mb = *p.MaxBytes
			}
			return readFileWorkspace(root, p.Path, mb, p.StartLine, p.EndLine)
		},
	})

	r.Register(Tool{
		Name: "list_dir",
		Description: "Lists files and directories under a path relative to the workspace. " +
			"max_depth 0 = no recursion (immediate children only); default max_depth 2. Results capped by max_entries.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory relative to workspace (default \".\").",
				},
				"max_depth": map[string]any{
					"type":        "integer",
					"description": "How many directory levels to recurse (default 2). Use 0 for flat listing only.",
				},
				"max_entries": map[string]any{
					"type":        "integer",
					"description": "Maximum entries to return (default 200).",
				},
			},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path       string `json:"path"`
				MaxDepth   *int   `json:"max_depth"`
				MaxEntries *int   `json:"max_entries"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			rel := p.Path
			if strings.TrimSpace(rel) == "" {
				rel = "."
			}
			md := defaultListMaxDepth
			if p.MaxDepth != nil {
				md = *p.MaxDepth
			}
			me := defaultListMaxEntries
			if p.MaxEntries != nil && *p.MaxEntries > 0 {
				me = *p.MaxEntries
			}
			return listDirWorkspace(root, rel, md, me)
		},
	})

	r.Register(Tool{
		Name: "search_files",
		Description: "Finds files under the workspace whose path or basename contains substring and/or ends with suffix. " +
			"Optional under limits the search to a subdirectory of the workspace.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"substring": map[string]any{
					"type":        "string",
					"description": "Match if relative path or filename contains this substring.",
				},
				"suffix": map[string]any{
					"type":        "string",
					"description": "Match if basename ends with this suffix (e.g. .go).",
				},
				"under": map[string]any{
					"type":        "string",
					"description": "Optional subdirectory relative to workspace to scope the search.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum paths to return (default 200).",
				},
			},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Substring   string `json:"substring"`
				Suffix      string `json:"suffix"`
				Under       string `json:"under"`
				MaxResults  *int   `json:"max_results"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			mr := defaultSearchMax
			if p.MaxResults != nil && *p.MaxResults > 0 {
				mr = *p.MaxResults
			}
			return searchFilesWorkspace(root, p.Under, p.Substring, p.Suffix, mr)
		},
	})

	r.Register(Tool{
		Name: "grep",
		Description: "Search file contents under the workspace. Uses ripgrep (rg) when installed; otherwise scans files with Go's regexp engine. " +
			"Prefer literal mode when searching fixed strings that contain regex metacharacters.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Search pattern (regex unless literal is true).",
				},
				"literal": map[string]any{
					"type":        "boolean",
					"description": "If true, treat pattern as fixed string (also passes --fixed-strings to rg).",
				},
				"path_prefix": map[string]any{
					"type":        "string",
					"description": "Optional subdirectory relative to workspace to search under.",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "Optional file glob e.g. *.go (passed to rg --glob when using ripgrep).",
				},
				"max_matches": map[string]any{
					"type":        "integer",
					"description": "Maximum matches to return (default 50, max 200).",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Pattern     string `json:"pattern"`
				Literal     bool   `json:"literal"`
				PathPrefix  string `json:"path_prefix"`
				Glob        string `json:"glob"`
				MaxMatches  *int   `json:"max_matches"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			max := defaultGrepMax
			if p.MaxMatches != nil && *p.MaxMatches > 0 {
				max = *p.MaxMatches
			}
			return grepWorkspace(ctx, root, p.PathPrefix, p.Pattern, p.Literal, p.Glob, max)
		},
	})
}

func registerWorkspaceMutatingTools(r *Registry, root string, exec *ExecOptions) {
	r.Register(Tool{
		Name: "write_file",
		Description: "Creates or overwrites a UTF-8 file under the workspace. " +
			"mode \"create\" fails if the file exists; \"overwrite\" (default) replaces content. Parent directories are created.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to workspace root.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full new file contents.",
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "create | overwrite (default overwrite).",
					"enum":        []string{"create", "overwrite"},
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
				Mode    string `json:"mode"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			mode := strings.TrimSpace(p.Mode)
			if mode == "" {
				mode = "overwrite"
			}
			if err := writeFileWorkspace(root, p.Path, p.Content, mode); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %s (%s)", p.Path, mode), nil
		},
	})

	if exec != nil && len(exec.Allowlist) > 0 {
		registerRunCommand(r, root, exec)
	}
}

func registerRunCommand(r *Registry, root string, exec *ExecOptions) {
	allow := allowSet(exec.Allowlist)
	timeout := time.Duration(exec.TimeoutSeconds) * time.Second
	maxOut := exec.MaxOutputBytes
	if maxOut < 1 {
		maxOut = 256 * 1024
	}

	r.Register(Tool{
		Name: "run_command",
		Description: "Runs a subprocess with working directory under the workspace. " +
			"argv[0] must be a bare command name on CODIENT_EXEC_ALLOWLIST (no paths). " +
			"Stdout and stderr are combined. Respects CODIENT_EXEC_TIMEOUT_SEC and output size limits.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"argv": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Program name first (no slashes), then arguments.",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Working directory relative to workspace (default \".\").",
				},
			},
			"required":             []string{"argv"},
			"additionalProperties": false,
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Argv []string `json:"argv"`
				Cwd  string   `json:"cwd"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return runCommand(ctx, root, p.Cwd, p.Argv, allow, timeout, maxOut)
		},
	})
}
