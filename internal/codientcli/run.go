// Package codientcli implements the codient command-line interface (REPL, single-turn,
// and auxiliary modes). The cmd/codient binary is a thin entrypoint that calls Run.
package codientcli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/agentlog"
	"codient/internal/assistout"
	"codient/internal/config"
	"codient/internal/designstore"
	"codient/internal/imageutil"
	"codient/internal/mcpclient"
	"codient/internal/openaiclient"
	"codient/internal/projectinfo"
	"codient/internal/prompt"
	"codient/internal/selfupdate"
	"codient/internal/tokentracker"
	"codient/internal/tools"
)

// Run parses flags and executes the codient CLI. It returns a process exit code.
func Run() int {
	var (
		system        = flag.String("system", "", "optional system prompt (merged into default tool-capabilities prompt)")
		promptFlag    = flag.String("prompt", "", "user message: without REPL, stdin is used if flag empty; with REPL, non-empty -prompt is the first turn")
		stream        = flag.Bool("stream", false, "single-turn streamed completion without tools (writes to stdout)")
		listModels    = flag.Bool("list-models", false, "print model ids from GET /v1/models and exit")
		listTools     = flag.Bool("list-tools", false, "print registered tool names for current env and exit")
		ping          = flag.Bool("ping", false, "check GET /v1/models and exit")
		timeout       = flag.Duration("timeout", 10*time.Minute, "per-invocation context timeout")
		goal          = flag.String("goal", "", "optional high-level objective; merged into task directive on first turn only")
		taskFile      = flag.String("task-file", "", "optional path to a task description file (capped at 32KiB); merged into task directive on first turn only")
		repl          = flag.Bool("repl", false, "multi-turn REPL (default when stdin is a TTY; kept for backward compatibility)")
		newSession    = flag.Bool("new-session", false, "start a fresh session instead of resuming the latest")
		logPath       = flag.String("log", "", "append JSONL agent events to this file")
		progress      = flag.Bool("progress", false, "print agent progress to stderr")
		modeFlag      = flag.String("mode", "", "build|ask|plan: tool + prompt policy (default: last REPL mode, else config, else build)")
		plainOut      = flag.Bool("plain", false, "print assistant replies as raw text (no markdown/ANSI)")
		streamReply   = flag.Bool("stream-reply", true, "stream assistant tokens to stdout")
		designSaveDir = flag.String("design-save-dir", "", "directory for saved implementation plans (default: <workspace>/.codient/plans)")
		workspace     = flag.String("workspace", "", "root directory for workspace tools (overrides config and cwd default)")
		a2aFlag       = flag.Bool("a2a", false, "start an A2A (Agent-to-Agent) protocol server instead of the CLI")
		a2aAddr       = flag.String("a2a-addr", ":8080", "listen address for the A2A server")
		showVersion   = flag.Bool("version", false, "print version and exit")
		update        = flag.Bool("update", false, "update codient to the latest release and exit")
		outputFormat  = flag.String("output-format", "text", "with -print: text|json|stream-json")
		approveStr    = flag.String("auto-approve", "off", "with -print: off|exec|fetch|all (non-interactive approvals)")
		maxTurns      = flag.Int("max-turns", 0, "max LLM rounds for one user turn (0=unlimited)")
		maxCostUSD    = flag.Float64("max-cost", 0, "max estimated session USD (0=unlimited; needs pricing)")
	)
	var (
		printMode bool
	)
	flag.BoolVar(&printMode, "print", false, "headless single-turn mode for CI (no REPL)")
	flag.BoolVar(&printMode, "p", false, "short for -print")
	var imageFlagPaths []string
	flag.Func("image", "attach image file(s): comma-separated paths; repeat -image for more (first REPL turn or single-shot; use with vision-capable models)", func(s string) error {
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				imageFlagPaths = append(imageFlagPaths, p)
			}
		}
		return nil
	})
	flag.Parse()

	selfupdate.CleanupOldBinary()

	if *showVersion {
		fmt.Println(Version)
		return 0
	}
	if *update {
		return runSelfUpdate()
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}

	// CLI flags override config file values when explicitly set.
	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	if explicit["workspace"] {
		cfg.Workspace = strings.TrimSpace(*workspace)
	}
	if explicit["mode"] {
		cfg.Mode = *modeFlag
	} else if lm := config.LoadLastMode(); lm != "" {
		cfg.Mode = lm
	}
	if explicit["plain"] {
		cfg.Plain = *plainOut
	}
	if explicit["progress"] {
		cfg.Progress = *progress
	}
	if explicit["stream-reply"] {
		cfg.StreamReply = *streamReply
	}
	if explicit["log"] {
		cfg.LogPath = *logPath
	}
	if explicit["design-save-dir"] {
		cfg.DesignSaveDir = *designSaveDir
	}

	agentMode, err := prompt.ParseMode(cfg.Mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mode: %v\n", err)
		return 2
	}

	outFmt, err := ParseOutputFormat(*outputFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "output-format: %v\n", err)
		return 2
	}
	autoPol, err := ParseAutoApprove(*approveStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auto-approve: %v\n", err)
		return 2
	}

	effectiveLog := strings.TrimSpace(cfg.LogPath)
	progressOut := resolveProgressOut(cfg.Progress, effectiveLog != "")

	// For quick commands and single-turn mode, use a wall-clock timeout.
	// For the REPL session, use a signal-based context so the user can
	// step away without hitting "context deadline exceeded".
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := openaiclient.New(cfg)

	// Quick commands that don't need a full session.
	if *ping {
		if err := client.PingModels(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "ping: %v\n", err)
			return 1
		}
		fmt.Println("ok")
		return 0
	}
	if *listModels {
		ids, err := client.ListModels(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "models: %v\n", err)
			return 1
		}
		for _, id := range ids {
			fmt.Println(id)
		}
		return 0
	}
	if *listTools {
		reg := buildRegistry(cfg, agentMode, nil, nil)
		for _, n := range reg.Names() {
			fmt.Println(n)
		}
		return 0
	}
	if *a2aFlag {
		cancel()
		a2aCtx, a2aCancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer a2aCancel()
		var agentLog *agentlog.Logger
		if effectiveLog != "" {
			logFile, err := os.OpenFile(effectiveLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "log: %v\n", err)
				return 2
			}
			defer logFile.Close()
			agentLog = agentlog.New(logFile)
		}
		return runA2AServer(a2aCtx, cfg, *a2aAddr, agentLog)
	}

	if *stream {
		user, err := resolvePrompt(*promptFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "prompt: %v\n", err)
			return 2
		}
		if strings.TrimSpace(user) == "" {
			fmt.Fprintf(os.Stderr, "provide -prompt or pipe a message on stdin\n")
			return 2
		}
		attached, err := loadImagePaths(imageFlagPaths)
		if err != nil {
			fmt.Fprintf(os.Stderr, "image: %v\n", err)
			return 2
		}
		return runBareStream(ctx, client, cfg.EffectiveWorkspace(), *system, user, attached)
	}

	var logFile *os.File
	var agentLog *agentlog.Logger
	if effectiveLog != "" {
		var err error
		logFile, err = os.OpenFile(effectiveLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "log: %v\n", err)
			return 2
		}
		defer logFile.Close()
	}
	switch {
	case printMode && outFmt == "stream-json":
		if logFile != nil {
			agentLog = agentlog.New(io.MultiWriter(logFile, os.Stdout))
		} else {
			agentLog = agentlog.New(os.Stdout)
		}
	case logFile != nil:
		agentLog = agentlog.New(logFile)
	}

	// Build the full agent session.
	repoInstr, err := prompt.LoadRepoInstructions(cfg.EffectiveWorkspace())
	if err != nil {
		fmt.Fprintf(os.Stderr, "repo instructions: %v\n", err)
		return 2
	}
	projectCtx := resolveProjectContext(cfg)

	stateDir, err := config.StateDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: state dir: %v\n", err)
	}
	mem, err := prompt.LoadMemory(stateDir, cfg.EffectiveWorkspace())
	if err != nil {
		fmt.Fprintf(os.Stderr, "memory: %v\n", err)
		return 2
	}
	var memOpts *tools.MemoryOptions
	if stateDir != "" || cfg.EffectiveWorkspace() != "" {
		memOpts = &tools.MemoryOptions{
			StateDir:      stateDir,
			WorkspaceRoot: cfg.EffectiveWorkspace(),
		}
	}

	var execAllow *tools.SessionExecAllow
	if len(cfg.ExecAllowlist) > 0 {
		execAllow = tools.NewSessionExecAllow(cfg.ExecAllowlist)
	}
	s := &session{
		cfg:              cfg,
		agentLog:         agentLog,
		progressOut:      progressOut,
		mode:             agentMode,
		richOutput:       assistantOutputRich(cfg.Plain),
		streamReply:      cfg.StreamReply,
		designSaveDir:    cfg.DesignSaveDir,
		goal:             *goal,
		taskFile:         *taskFile,
		userSystem:       *system,
		repoInstructions: repoInstr,
		projectContext:   projectCtx,
		memory:           mem,
		memOpts:          memOpts,
		execAllow:        execAllow,
		tokenTracker:     &tokentracker.Tracker{},
		printMode:        printMode,
		outputFormat:     outFmt,
		autoApprove:      autoPol,
		maxTurns:         *maxTurns,
		maxCostUSD:       *maxCostUSD,
	}
	if len(cfg.MCPServers) > 0 {
		mgr := mcpclient.NewManager(Version)
		warns := mgr.Connect(ctx, cfg.MCPServers)
		for _, w := range warns {
			fmt.Fprintf(os.Stderr, "codient: %s\n", w)
		}
		if len(mgr.ServerIDs()) > 0 {
			s.mcpMgr = mgr
		}
	}

	s.client = openaiclient.New(cfg)
	s.registry = buildRegistry(cfg, agentMode, s, memOpts)
	s.systemPrompt = buildAgentSystemPrompt(cfg, s.registry, agentMode, *system, repoInstr, projectCtx, mem, effectiveAutoCheckCmd(cfg))

	attached, err := loadImagePaths(imageFlagPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "image: %v\n", err)
		return 2
	}
	s.initialImages = attached

	// Determine whether to enter the REPL session.
	// REPL is the default when stdin is a TTY (interactive), or when -repl is explicit.
	// -print forces single-turn (headless) mode.
	stdinIsTTY := stdinIsInteractive()
	useREPL := !printMode && (*repl || (stdinIsTTY && strings.TrimSpace(*promptFlag) == ""))

	if useREPL {
		// Override the timeout context with a signal-based one for the REPL.
		// The session can last indefinitely; only Ctrl+C should cancel it.
		cancel()

		if stdinIsTTY && !cfg.Plain {
			// In TUI mode, Bubble Tea owns signal handling (Ctrl+C → KeyCtrlC).
			// Use a manually cancellable context instead of signal.NotifyContext
			// to avoid conflicts with Bubble Tea's input reading.
			tuiCtx, tuiCancel := context.WithCancel(context.Background())
			defer tuiCancel()

			ts, err := initTUI(string(agentMode), cfg.Plain)
			if err != nil {
				fmt.Fprintf(os.Stderr, "tui: %v\n", err)
				replCtx, replCancel := signal.NotifyContext(context.Background(), os.Interrupt)
				defer replCancel()
				return s.runSession(replCtx, *promptFlag, *newSession)
			}
			s.tui = ts
			// Re-resolve progressOut now that os.Stderr points to the TUI pipe.
			// The original resolveProgressOut captured the real stderr fd before
			// initTUI redirected it, so the session's progress writer must be
			// updated to write through the pipe into the viewport.
			if s.progressOut != nil {
				s.progressOut = os.Stderr
			}
			ts.startPipeReaders()
			go func() {
				defer close(ts.done)
				defer func() {
					if r := recover(); r != nil {
						fmt.Fprintf(ts.origErr, "codient: session panic: %v\n", r)
						ts.exitCode = 1
					}
				}()
				ts.exitCode = s.runSession(tuiCtx, *promptFlag, *newSession)
				// Session finished normally — close write-ends so pipe readers
				// see EOF, then tell the TUI to quit.
				ts.stdoutW.Close()
				ts.stderrW.Close()
				ts.prog.Send(tuiQuitMsg{exitCode: ts.exitCode})
			}()
			if _, err := ts.prog.Run(); err != nil {
				tuiCancel()
				ts.input.Close() // unblock chanReader so session goroutine exits
				<-ts.done
				ts.cleanup()
				fmt.Fprintf(ts.origErr, "tui: %v\n", err)
				return 1
			}
			// TUI exited (user pressed Ctrl+C or session sent quit).
			// Cancel the session context and close input to unblock the goroutine.
			tuiCancel()
			ts.input.Close()
			<-ts.done
			code := ts.exitCode
			ts.cleanup()
			return code
		}

		replCtx, replCancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer replCancel()
		return s.runSession(replCtx, *promptFlag, *newSession)
	}

	// Single-turn mode (piped input or explicit -prompt without -repl).
	if err := cfg.RequireModel(); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}
	user, err := resolvePrompt(*promptFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prompt: %v\n", err)
		return 2
	}
	if strings.TrimSpace(user) == "" {
		fmt.Fprintf(os.Stderr, "provide -prompt or pipe a message on stdin\n")
		return 2
	}
	return s.runSingleTurn(ctx, user, attached)
}

func stdinIsInteractive() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func runBareStream(ctx context.Context, client *openaiclient.Client, workspace, system, user string, attached []imageutil.ImageAttachment) int {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, openai.SystemMessage(strings.TrimSpace(system)))
	}
	userMsg, err := buildUserMessage(workspace, user, attached)
	if err != nil {
		fmt.Fprintf(os.Stderr, "image: %v\n", err)
		return 2
	}
	msgs = append(msgs, userMsg)
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(client.Model()),
		Messages: msgs,
	}
	res, err := client.StreamChatCompletion(ctx, params, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nstream: %v\n", err)
		return 1
	}
	if res != nil {
		u := res.Usage
		if u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0 {
			fmt.Fprintf(os.Stderr, "\ntokens: %d prompt / %d completion / %d total\n",
				u.PromptTokens, u.CompletionTokens, u.TotalTokens)
		}
	}
	fmt.Fprintln(os.Stdout)
	return 0
}

func assistantOutputRich(plain bool) bool {
	if plain {
		return false
	}
	return assistout.StdoutIsInteractive()
}

func resolveProgressOut(progressCfg, logRequested bool) io.Writer {
	if progressCfg {
		return os.Stderr
	}
	if logRequested {
		return os.Stderr
	}
	st, err := os.Stderr.Stat()
	if err != nil {
		return nil
	}
	if (st.Mode() & os.ModeCharDevice) != 0 {
		return os.Stderr
	}
	return nil
}

func resolveStreamReply(cfgStreamReply bool, stdoutTTY bool) bool {
	return cfgStreamReply && stdoutTTY
}

func streamWriterForTurn(streamReplyVal bool, stdoutTTY bool, mode prompt.Mode, richAssistant bool, lastAssistantReply string) io.Writer {
	if !resolveStreamReply(streamReplyVal, stdoutTTY) {
		return nil
	}
	if mode == prompt.ModePlan && richAssistant && assistout.ReplySignalsPlanWait(lastAssistantReply) {
		return nil
	}
	return os.Stdout
}

func writePlanDraftPreamble(w io.Writer, mode prompt.Mode, lastAssistantReply string) {
	if mode != prompt.ModePlan || !assistout.ReplySignalsPlanWait(lastAssistantReply) {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Building the implementation plan…")
}

func finishAssistantTurn(w io.Writer, reply string, useMarkdown, planMode, streamed bool) error {
	if streamed {
		_, err := fmt.Fprintln(w)
		return err
	}
	return assistout.WriteAssistant(w, reply, useMarkdown, planMode)
}

func maybeSaveDesign(stderr io.Writer, workspace, designSaveDir, sessionID string, mode prompt.Mode, reply string, taskSlug string, designSave bool) {
	if mode != prompt.ModePlan {
		return
	}
	if !designSave {
		return
	}
	text := assistout.PrepareAssistantText(reply, true)
	if !designstore.LooksLikeReadyToImplement(text) {
		return
	}
	path, err := designstore.Save(workspace, designSaveDir, sessionID, taskSlug, text, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "codient: saving design: %v\n", err)
		return
	}
	fmt.Fprintf(stderr, "codient: wrote design to %s\n", path)
}

func resolveProjectContext(cfg *config.Config) string {
	if strings.EqualFold(strings.TrimSpace(cfg.ProjectContext), "off") {
		return ""
	}
	return projectinfo.Detect(cfg.EffectiveWorkspace())
}

func runSelfUpdate() int {
	fmt.Fprintf(os.Stderr, "codient: checking for updates...\n")
	tag, err := selfupdate.LatestVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: %v\n", err)
		return 1
	}
	if !selfupdate.IsNewer(Version, tag) {
		fmt.Fprintf(os.Stderr, "codient: already up to date (%s)\n", Version)
		return 0
	}
	newVer := strings.TrimPrefix(tag, "v")
	fmt.Fprintf(os.Stderr, "codient: updating %s -> %s...\n", Version, newVer)
	if err := selfupdate.Apply(tag); err != nil {
		fmt.Fprintf(os.Stderr, "codient: update failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "codient: updated to %s\n", newVer)
	return 0
}

func resolvePrompt(flagPrompt string) (string, error) {
	if strings.TrimSpace(flagPrompt) != "" {
		return flagPrompt, nil
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
