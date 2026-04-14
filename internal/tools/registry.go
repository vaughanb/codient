// Package tools registers callable functions exposed to the LLM as OpenAI-style tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"codient/internal/codeindex"

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

// Run executes a tool by name. Unknown names return a structured error string for the model
// that includes the list of available tools to help it self-correct.
func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.mu.RLock()
	t, ok := r.by[name]
	names := r.order
	r.mu.RUnlock()
	if !ok {
		if strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("tool call has an empty name; available tools: %s", strings.Join(names, ", "))
		}
		return "", fmt.Errorf("unknown tool %q; available tools: %s", name, strings.Join(names, ", "))
	}
	return t.Run(ctx, args)
}

// Default returns a registry with safe builtins and, when workspace is non-empty,
// coding tools scoped to that directory (config workspace or -workspace).
// exec enables run_command when non-nil and Allowlist is non-empty (exec_allowlist in config).
// fetch enables fetch_url when non-nil and AllowHosts is non-empty (fetch_allow_hosts / preapproved in config).
// search enables web_search when non-nil (always enabled in default builds).
func Default(workspace string, exec *ExecOptions, fetch *FetchOptions, search *SearchOptions, astGrepPath string, idx *codeindex.Index, mem *MemoryOptions) *Registry {
	r := NewRegistry()
	registerBuiltinTools(r, true)
	root := strings.TrimSpace(workspace)
	if root != "" {
		registerWorkspaceTools(r, root, exec, fetch, search, astGrepPath)
	}
	registerMemoryUpdate(r, mem)
	registerSemanticSearch(r, idx)
	return r
}

// DefaultReadOnly is like Default but omits write_file and run_command: read/search/list/grep
// only (plus echo and get_time). Use for Ask mode.
// fetch enables fetch_url when non-nil and AllowHosts is non-empty.
// search enables web_search when non-nil.
func DefaultReadOnly(workspace string, fetch *FetchOptions, search *SearchOptions, astGrepPath string, idx *codeindex.Index) *Registry {
	r := NewRegistry()
	registerBuiltinTools(r, true)
	root := strings.TrimSpace(workspace)
	if root != "" {
		registerWorkspaceReadTools(r, root, fetch, search, astGrepPath)
	}
	registerSemanticSearch(r, idx)
	return r
}

// DefaultReadOnlyPlan is like DefaultReadOnly but omits echo so the model cannot substitute
// a one-line echo for a written design. Use for Plan mode.
func DefaultReadOnlyPlan(workspace string, fetch *FetchOptions, search *SearchOptions, astGrepPath string, idx *codeindex.Index) *Registry {
	r := NewRegistry()
	registerBuiltinTools(r, false)
	root := strings.TrimSpace(workspace)
	if root != "" {
		registerWorkspaceReadTools(r, root, fetch, search, astGrepPath)
	}
	registerSemanticSearch(r, idx)
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

func registerWorkspaceTools(r *Registry, root string, exec *ExecOptions, fetch *FetchOptions, search *SearchOptions, astGrepPath string) {
	registerWorkspaceReadTools(r, root, fetch, search, astGrepPath)
	registerWorkspaceMutatingTools(r, root, exec)
}

func registerWorkspaceReadTools(r *Registry, root string, fetch *FetchOptions, search *SearchOptions, astGrepPath string) {
	r.Register(Tool{
		Name: "read_file",
		Description: "Reads a UTF-8 text file under the configured workspace root. " +
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
			"At least one of substring or suffix MUST be provided. " +
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

	r.Register(Tool{
		Name: "path_stat",
		Description: "Returns metadata for a path under the workspace without reading file contents: " +
			"exists, file/directory/symlink, size, mode, mod_time. Use before read_file when you only need presence or size.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to workspace root.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return pathStatWorkspace(root, p.Path)
		},
	})

	r.Register(Tool{
		Name: "glob_files",
		Description: "Lists files under a subdirectory matching a glob pattern. " +
			"If pattern contains '/', it is matched against the path relative to under (forward slashes). " +
			"Otherwise the pattern matches each file's basename only (recursive). " +
			"Example basename patterns: *_test.go, *.md. Results capped by max_results.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"under": map[string]any{
					"type":        "string",
					"description": "Directory relative to workspace (default \".\").",
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern (see tool description).",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum paths to return (default 200).",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Under       string `json:"under"`
				Pattern     string `json:"pattern"`
				MaxResults  *int   `json:"max_results"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			under := p.Under
			if strings.TrimSpace(under) == "" {
				under = "."
			}
			mr := defaultGlobMaxResults
			if p.MaxResults != nil && *p.MaxResults > 0 {
				mr = *p.MaxResults
			}
			return globFilesWorkspace(root, under, p.Pattern, mr)
		},
	})

	registerFetchURL(r, fetch)
	registerWebSearch(r, search, fetch)
	registerAstGrepTools(r, root, astGrepPath)
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
			if p.Content == "" {
				return "", fmt.Errorf("content is empty; write_file requires non-empty content")
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

	r.Register(Tool{
		Name: "ensure_dir",
		Description: "Creates a directory under the workspace (and parent directories as needed). " +
			"Uses the same path rules as write_file; portable across Windows, macOS, and Linux—prefer this over shell mkdir.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path relative to workspace root (e.g. \"cmd\" or \"internal/pkg/widget\").",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if err := ensureDirWorkspace(root, p.Path); err != nil {
				return "", err
			}
			return fmt.Sprintf("created directory %s", strings.TrimSpace(p.Path)), nil
		},
	})

	r.Register(Tool{
		Name: "insert_lines",
		Description: "Insert text into an existing file at a given line position, or append to the end (default). " +
			"Best tool for adding new functions, test cases, or blocks to the end of a file. " +
			"Use position \"end\" (default) to append, \"beginning\" to prepend, or after_line for a specific 1-based line.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to workspace root.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Text to insert.",
				},
				"position": map[string]any{
					"type":        "string",
					"description": "Where to insert: \"end\" (default) or \"beginning\". Ignored when after_line is set.",
					"enum":        []string{"end", "beginning"},
				},
				"after_line": map[string]any{
					"type":        "integer",
					"description": "1-based line number; content is inserted after this line. 0 means prepend. Overrides position.",
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path      string `json:"path"`
				Content   string `json:"content"`
				Position  string `json:"position"`
				AfterLine int    `json:"after_line"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return insertLinesWorkspace(root, p.Path, p.Content, p.Position, p.AfterLine)
		},
	})

	r.Register(Tool{
		Name: "str_replace",
		Description: "Targeted edit: replace an exact string in a file. " +
			"Provide enough context in old_string to make the match unique. " +
			"Fails when old_string matches 0 or >1 locations (unless replace_all is true). " +
			"Prefer this over write_file for editing existing files.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to workspace root.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "Exact text to find (include surrounding lines for uniqueness).",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "Replacement text.",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace all occurrences (default false).",
				},
			},
			"required":             []string{"path", "old_string", "new_string"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path       string `json:"path"`
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return strReplaceWorkspace(root, p.Path, p.OldString, p.NewString, p.ReplaceAll)
		},
	})

	r.Register(Tool{
		Name: "patch_file",
		Description: "Apply a unified diff to an existing UTF-8 file. " +
			"More compact than write_file for multi-site edits on large files. " +
			"The diff parameter is the unified diff body (@@ hunk headers plus context / + / - lines). " +
			"Context lines must match the current file. " +
			"Prefer str_replace for single-site edits; use patch_file when changing " +
			"multiple locations in one call or when the edit spans many lines.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to workspace root.",
				},
				"diff": map[string]any{
					"type":        "string",
					"description": "Unified diff body (hunk headers + context/add/remove lines).",
				},
			},
			"required":             []string{"path", "diff"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
				Diff string `json:"diff"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return patchFileWorkspace(root, p.Path, p.Diff)
		},
	})

	r.Register(Tool{
		Name: "remove_path",
		Description: "Deletes a file or empty/non-empty directory tree under the workspace (same semantics as rm -rf). " +
			"Paths are relative to the workspace root.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File or directory relative to workspace root.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if err := removePathWorkspace(root, p.Path); err != nil {
				return "", err
			}
			return fmt.Sprintf("removed %s", p.Path), nil
		},
	})

	r.Register(Tool{
		Name: "move_path",
		Description: "Moves or renames a file or directory within the workspace (from -> to). " +
			"Destination parent directories are created when needed.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"from": map[string]any{
					"type":        "string",
					"description": "Source path relative to workspace.",
				},
				"to": map[string]any{
					"type":        "string",
					"description": "Destination path relative to workspace.",
				},
			},
			"required":             []string{"from", "to"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if err := movePathWorkspace(root, p.From, p.To); err != nil {
				return "", err
			}
			return fmt.Sprintf("moved %s -> %s", p.From, p.To), nil
		},
	})

	r.Register(Tool{
		Name: "copy_path",
		Description: "Copies a file or directory tree within the workspace (from -> to). " +
			"Symlinks are not supported. Existing destination files are overwritten.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"from": map[string]any{
					"type":        "string",
					"description": "Source path relative to workspace.",
				},
				"to": map[string]any{
					"type":        "string",
					"description": "Destination path relative to workspace.",
				},
			},
			"required":             []string{"from", "to"},
			"additionalProperties": false,
		},
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if err := copyPathWorkspace(root, p.From, p.To); err != nil {
				return "", err
			}
			return fmt.Sprintf("copied %s -> %s", p.From, p.To), nil
		},
	})

	if exec != nil && (len(exec.Allowlist) > 0 || exec.Session != nil) {
		registerRunCommand(r, root, exec)
		registerRunShell(r, root, exec)
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
			"argv[0] must be a command name on the allowlist; a leading ./ or .\\ prefix is accepted " +
			"and resolves the binary relative to the working directory. " +
			"For shell builtins (mkdir, redirects, pipelines) use run_shell instead. " +
			"Default allowlist includes go, git, and the platform shell; override with exec_allowlist in ~/.codient/config.json or /config; disable with exec_disable. " +
			"If a command is not allowlisted, the user may be prompted to allow it for this session. " +
			"Stdout and stderr are combined. Respects exec_timeout_sec and exec_max_output_bytes in config.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"argv": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Program name first (bare name or ./name for workspace-relative), then arguments.",
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
			if exec.Session != nil {
				return runCommandWithSession(ctx, exec, root, p.Cwd, p.Argv, timeout, maxOut)
			}
			return runCommand(ctx, root, p.Cwd, p.Argv, allow, timeout, maxOut, exec.ProgressWriter)
		},
	})
}

func registerRunShell(r *Registry, root string, exec *ExecOptions) {
	allow := allowSet(exec.Allowlist)
	timeout := time.Duration(exec.TimeoutSeconds) * time.Second
	maxOut := exec.MaxOutputBytes
	if maxOut < 1 {
		maxOut = 256 * 1024
	}

	r.Register(Tool{
		Name: "run_shell",
		Description: "Runs one shell command line under the workspace (Windows: cmd /c; Unix: sh -c). " +
			"Use this for shell builtins (mkdir, rmdir), pipelines, environment variable expansion, and scripts. " +
			"Prefer run_command for a single external program (e.g. go, git). " +
			"The shell binary (cmd or sh) must be allowlisted; same session rules as run_command.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Full command line passed to the shell (e.g. \"mkdir internal\" or \"go test ./...\").",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Working directory relative to workspace (default \".\").",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Command string `json:"command"`
				Cwd     string `json:"cwd"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			argv, err := shellArgv(p.Command)
			if err != nil {
				return "", err
			}
			if exec.Session != nil {
				return runCommandWithSession(ctx, exec, root, p.Cwd, argv, timeout, maxOut)
			}
			return runCommand(ctx, root, p.Cwd, argv, allow, timeout, maxOut, exec.ProgressWriter)
		},
	})
}
