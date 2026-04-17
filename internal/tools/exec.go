package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"codient/internal/sandbox"
)

// ExecPromptChoice is the user's response when a command is not on the session allowlist.
type ExecPromptChoice int

const (
	// ExecPromptDeny rejects the command; run_command returns an error.
	ExecPromptDeny ExecPromptChoice = iota
	// ExecPromptAllowSession adds the command name to the session allowlist and proceeds.
	ExecPromptAllowSession
	// ExecPromptAllowAll permits all commands for the rest of the session (no further prompts).
	ExecPromptAllowAll
)

// ExecOptions configures the run_command tool. Pass nil to Default to disable it.
type ExecOptions struct {
	Allowlist      []string
	Session        *SessionExecAllow
	TimeoutSeconds int
	MaxOutputBytes int
	// ProgressWriter, when non-nil, receives live subprocess output lines
	// (prefixed with "  | ") while the command runs. Typically stderr.
	ProgressWriter io.Writer
	// PromptOnDenied is called when a command is not allowlisted. If nil, denial is a hard error.
	PromptOnDenied func(ctx context.Context, deniedKey string, argv []string) ExecPromptChoice
	promptMu       sync.Mutex
	// EnvPassthrough lists extra environment variable names forwarded to subprocesses (after scrubbing).
	EnvPassthrough []string
	// SandboxReadOnlyPaths are extra read-only paths for native/container sandboxes.
	SandboxReadOnlyPaths []string
	// SandboxRunner executes the subprocess; nil means sandbox.NoopRunner (env scrub only).
	SandboxRunner sandbox.Runner
	// WorkspaceRoot is the configured workspace root (for sandbox policy). Empty uses workDir only.
	WorkspaceRoot string
}

// LineStreamer is an io.Writer that captures all bytes into an internal buffer
// (for the final tool result) and simultaneously emits complete lines with a
// "  | " prefix to an optional progress writer (for live human-readable output).
type LineStreamer struct {
	buf      bytes.Buffer
	progress io.Writer
	partial  []byte
}

// NewLineStreamer returns a writer that tees to progress with line prefixes.
// If progress is nil, it behaves as a plain bytes.Buffer.
func NewLineStreamer(progress io.Writer) *LineStreamer {
	return &LineStreamer{progress: progress}
}

func (ls *LineStreamer) Write(p []byte) (int, error) {
	ls.buf.Write(p)
	if ls.progress == nil {
		return len(p), nil
	}
	ls.partial = append(ls.partial, p...)
	for {
		idx := bytes.IndexByte(ls.partial, '\n')
		if idx < 0 {
			break
		}
		line := ls.partial[:idx]
		ls.partial = ls.partial[idx+1:]
		fmt.Fprintf(ls.progress, "  | %s\n", line)
	}
	return len(p), nil
}

// Flush emits any remaining partial line to the progress writer.
func (ls *LineStreamer) Flush() {
	if ls.progress != nil && len(ls.partial) > 0 {
		fmt.Fprintf(ls.progress, "  | %s\n", ls.partial)
		ls.partial = nil
	}
}

// Bytes returns the full captured output.
func (ls *LineStreamer) Bytes() []byte {
	return ls.buf.Bytes()
}

func allowSet(allow []string) map[string]struct{} {
	m := make(map[string]struct{}, len(allow))
	for _, a := range allow {
		a = strings.TrimSpace(strings.ToLower(a))
		a = strings.TrimSuffix(a, ".exe")
		a = strings.TrimSuffix(a, ".bat")
		a = strings.TrimSuffix(a, ".cmd")
		if a != "" {
			m[a] = struct{}{}
		}
	}
	return m
}

// stripDotSlash removes a leading "./" or ".\" from a command name so that
// invocations like "./myapp.exe" are treated as "myapp.exe" resolved relative
// to the working directory. Returns the cleaned name and whether a prefix was stripped.
func stripDotSlash(name string) (string, bool) {
	if strings.HasPrefix(name, "./") || strings.HasPrefix(name, ".\\") {
		return name[2:], true
	}
	return name, false
}

// resolveExec finds the executable path. When dotRel is true the binary is
// resolved relative to workDir (the model wrote "./foo"); otherwise exec.LookPath
// searches PATH as usual.
func resolveExec(name string, dotRel bool, workDir string) (string, error) {
	if dotRel && workDir != "" {
		p := filepath.Join(workDir, name)
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("cannot find %q in working directory: %w", name, err)
		}
		return p, nil
	}
	look, err := exec.LookPath(name)
	if err != nil {
		return "", lookPathErr(name, err)
	}
	return look, nil
}

// ensureExecAllowedAndResolve checks the session allowlist (prompting before any LookPath
// so unknown commands still trigger a permission prompt). A leading "./" or ".\" on
// argv[0] is stripped and the binary is resolved relative to workDir instead of PATH.
// Returns the resolved executable path.
func ensureExecAllowedAndResolve(ctx context.Context, opt *ExecOptions, argv []string, workDir string) (look string, err error) {
	if opt == nil || opt.Session == nil {
		return "", fmt.Errorf("internal: session exec options required")
	}
	if len(argv) == 0 {
		return "", fmt.Errorf("argv must be non-empty")
	}
	name, dotRel := stripDotSlash(argv[0])
	sa := opt.Session
	for {
		if sa.AllowAll() {
			return resolveExec(name, dotRel, workDir)
		}
		if strings.ContainsAny(name, `/\`) {
			return "", fmt.Errorf("argv[0] must be a command name without path separators (got %q)", argv[0])
		}
		k0 := NormalizeCmdKey(name)

		if !sa.IsAllowed(k0) {
			if opt.PromptOnDenied == nil {
				return "", fmt.Errorf("command %q is not on the exec allowlist", k0)
			}
			opt.promptMu.Lock()
			choice := opt.PromptOnDenied(ctx, k0, argv)
			opt.promptMu.Unlock()
			switch choice {
			case ExecPromptDeny:
				return "", fmt.Errorf("user denied permission to run %q", k0)
			case ExecPromptAllowSession:
				sa.Add(k0)
				continue
			case ExecPromptAllowAll:
				sa.SetAllowAll()
				return resolveExec(name, dotRel, workDir)
			default:
				return "", fmt.Errorf("user denied permission to run %q", k0)
			}
		}

		look, err := resolveExec(name, dotRel, workDir)
		if err != nil {
			return "", err
		}
		rk := NormalizeCmdKey(look)
		if sa.IsAllowed(rk) {
			return look, nil
		}
		if opt.PromptOnDenied == nil {
			return "", fmt.Errorf("resolved binary %q is not on the exec allowlist", rk)
		}
		opt.promptMu.Lock()
		choice := opt.PromptOnDenied(ctx, rk, argv)
		opt.promptMu.Unlock()
		switch choice {
		case ExecPromptDeny:
			return "", fmt.Errorf("user denied permission to run %q", rk)
		case ExecPromptAllowSession:
			sa.Add(rk)
			continue
		case ExecPromptAllowAll:
			sa.SetAllowAll()
			return look, nil
		default:
			return "", fmt.Errorf("user denied permission to run %q", rk)
		}
	}
}

func lookPathErr(name string, err error) error {
	return fmt.Errorf("cannot find executable %q: %w%s", name, err, lookPathHint(name))
}

func lookPathHint(name string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	switch strings.ToLower(NormalizeCmdKey(name)) {
	case "mkdir", "rmdir", "cd", "dir", "copy", "move", "del", "type", "cls":
		return " — on Windows this is usually a shell builtin, not a standalone program in PATH; " +
			"prefer write_file (parent directories are created automatically) or use cmd.exe with /c (e.g. cmd /c mkdir ...)"
	default:
		return ""
	}
}

// ShellArgv builds argv for running a command via the platform shell:
// Windows uses cmd /c; Unix uses sh -c.
func ShellArgv(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("command is empty")
	}
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", line}, nil
	}
	return []string{"sh", "-c", line}, nil
}

func runCommandWithSession(ctx context.Context, opt *ExecOptions, workspaceRoot, cwdRel string, argv []string, timeout time.Duration, maxOut int) (string, error) {
	cwd := strings.TrimSpace(cwdRel)
	if cwd == "" {
		cwd = "."
	}
	workDir, err := absUnderRoot(workspaceRoot, cwd)
	if err != nil {
		return "", err
	}
	look, err := ensureExecAllowedAndResolve(ctx, opt, argv, workDir)
	if err != nil {
		return "", err
	}
	return executeSubprocess(ctx, opt, workspaceRoot, workDir, look, argv, timeout, maxOut, opt.ProgressWriter)
}

func executeSubprocess(ctx context.Context, opt *ExecOptions, workspaceRoot, workDir, look string, argv []string, timeout time.Duration, maxOut int, progress io.Writer) (string, error) {
	runner := sandbox.Runner(sandbox.NoopRunner{})
	if opt != nil && opt.SandboxRunner != nil {
		runner = opt.SandboxRunner
	}
	env := execEnv(opt)
	pol := execPolicy(opt, workspaceRoot, workDir)
	fullArgv := append([]string{look}, argv[1:]...)

	var out []byte
	var exitCode int
	if progress != nil {
		ls := NewLineStreamer(progress)
		var runErr error
		exitCode, runErr = runner.Exec(ctx, pol, workDir, fullArgv, env, timeout, ls, ls)
		ls.Flush()
		out = ls.Bytes()
		if runErr != nil {
			return "", runErr
		}
	} else {
		var buf bytes.Buffer
		var runErr error
		exitCode, runErr = runner.Exec(ctx, pol, workDir, fullArgv, env, timeout, &buf, &buf)
		out = buf.Bytes()
		if runErr != nil {
			return "", runErr
		}
	}

	trunc := ""
	if maxOut > 0 && len(out) > maxOut {
		out = out[:maxOut]
		trunc = fmt.Sprintf("\n\n[truncated output at %d bytes]", maxOut)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\n", exitCode)
	fmt.Fprintf(&b, "cwd: %s\n", workDir)
	fmt.Fprintf(&b, "argv: %q\n\n", argv)
	b.Write(out)
	b.WriteString(trunc)
	return b.String(), nil
}

func execPolicy(opt *ExecOptions, workspaceRoot, workDir string) sandbox.Policy {
	p := sandbox.Policy{}
	if opt != nil {
		p.EnvPassthrough = opt.EnvPassthrough
		p.ReadOnlyPaths = append([]string(nil), opt.SandboxReadOnlyPaths...)
	}
	ws := workspaceRoot
	if strings.TrimSpace(ws) == "" {
		ws = workDir
	}
	if aw, err := filepath.Abs(ws); err == nil {
		p.ReadWritePaths = append(p.ReadWritePaths, aw)
	}
	if wd, err := filepath.Abs(workDir); err == nil {
		if len(p.ReadWritePaths) == 0 || p.ReadWritePaths[len(p.ReadWritePaths)-1] != wd {
			p.ReadWritePaths = append(p.ReadWritePaths, wd)
		}
	}
	return p
}

func runCommand(ctx context.Context, workspaceRoot, cwdRel string, argv []string, allow map[string]struct{}, opt *ExecOptions, timeout time.Duration, maxOut int, progress io.Writer) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("argv must be non-empty")
	}
	name, dotRel := stripDotSlash(argv[0])
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("argv[0] must be a command name without path separators (got %q)", argv[0])
	}
	key := NormalizeCmdKey(name)
	if key == "" {
		return "", fmt.Errorf("empty command name")
	}
	if _, ok := allow[key]; !ok {
		return "", fmt.Errorf("command %q is not on the exec allowlist (configure exec_allowlist in ~/.codient/config.json or /config)", key)
	}

	cwd := strings.TrimSpace(cwdRel)
	if cwd == "" {
		cwd = "."
	}
	workDir, err := absUnderRoot(workspaceRoot, cwd)
	if err != nil {
		return "", err
	}

	look, err := resolveExec(name, dotRel, workDir)
	if err != nil {
		return "", err
	}
	if !dotRel {
		resolvedKey := NormalizeCmdKey(look)
		if _, ok := allow[resolvedKey]; !ok {
			return "", fmt.Errorf("resolved binary %q is not allowlisted", resolvedKey)
		}
	}

	return executeSubprocess(ctx, opt, workspaceRoot, workDir, look, argv, timeout, maxOut, progress)
}

func execEnv(opt *ExecOptions) []string {
	pt := []string(nil)
	if opt != nil {
		pt = opt.EnvPassthrough
	}
	return sandbox.ScrubEnv(osEnviron(), pt)
}

// osEnviron exists for tests to stub.
var osEnviron = func() []string { return os.Environ() }
